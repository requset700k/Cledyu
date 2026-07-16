// Discord Interactions 엔드포인트 — 버튼/드롭다운 클릭 수신.
//
// ⚠️ Ed25519 서명 검증은 Discord 의 강제 요구다. Discord 는 무작위로 "가짜 서명" 요청을 보내
// 테스트하고, 401 을 뱉지 않으면 Interactions Endpoint URL 을 등록 해제한다(공식 문서).
// python3.12 표준 라이브러리엔 Ed25519 가 없어 이 함수만 nodejs20 이다(설계 §3.4).
import { createPublicKey, verify } from 'node:crypto';
import { DynamoDBClient, GetItemCommand, UpdateItemCommand } from '@aws-sdk/client-dynamodb';
import { SFNClient, SendTaskSuccessCommand } from '@aws-sdk/client-sfn';
import { SecretsManagerClient, GetSecretValueCommand } from '@aws-sdk/client-secrets-manager';

const ddb = new DynamoDBClient({});
const sfn = new SFNClient({});
const sm = new SecretsManagerClient({});

const APPROVERS = JSON.parse(process.env.APPROVER_IDS || '[]');
const TABLE = process.env.APPROVALS_TABLE;

// Discord 공개키는 hex 문자열. Node 의 createPublicKey 는 raw 키를 직접 받지 않으므로
// Ed25519 SPKI DER 접두(12바이트) + 32바이트 raw 공개키로 DER 을 조립해 넘긴다.
const SPKI_PREFIX = Buffer.from('302a300506032b6570032100', 'hex');
let cachedKey = null;

async function publicKey() {
  if (cachedKey) return cachedKey;
  const resp = await sm.send(
    new GetSecretValueCommand({ SecretId: process.env.PUBKEY_SECRET_ARN }),
  );
  const hex = JSON.parse(resp.SecretString).public_key;
  const der = Buffer.concat([SPKI_PREFIX, Buffer.from(hex, 'hex')]);
  cachedKey = createPublicKey({ key: der, format: 'der', type: 'spki' });
  return cachedKey;
}

function res(status, body) {
  return {
    statusCode: status,
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  };
}

// 원본 메시지의 버튼을 비활성화하고 승인 기록을 남긴다. 재클릭 방지 + 채널에 감사 흔적.
function disabledMessage(original, who) {
  const stamp = new Date().toISOString().replace('T', ' ').slice(0, 19);
  return {
    type: 7, // UPDATE_MESSAGE
    data: {
      content: `${original}\n\n✅ <@${who}> 가 승인함 · ${stamp} UTC`,
      components: [],
    },
  };
}

export const handler = async (event) => {
  const sig = event.headers?.['x-signature-ed25519'];
  const ts = event.headers?.['x-signature-timestamp'];
  const raw = event.body || '';

  if (!sig || !ts) return res(401, { error: 'missing signature headers' });

  let ok = false;
  try {
    ok = verify(null, Buffer.from(ts + raw), await publicKey(), Buffer.from(sig, 'hex'));
  } catch {
    ok = false;
  }
  if (!ok) return res(401, { error: 'invalid signature' });

  const body = JSON.parse(raw);

  // type 1 = PING. Discord 가 엔드포인트 등록 시 보낸다 — PONG 으로 답해야 저장이 성공한다.
  if (body.type === 1) return res(200, { type: 1 });

  // type 3 = MESSAGE_COMPONENT (버튼/드롭다운)
  if (body.type !== 3) return res(200, { type: 4, data: { content: '지원하지 않는 상호작용' } });

  const customId = body.data.custom_id || '';
  const [kind, approvalId] = customId.split(':');
  const userId = body.member?.user?.id ?? body.user?.id;

  // 허용목록을 모든 컴포넌트 분기보다 위에 둔다. 서명 검증만으로는 채널을 보는 누구나 누를 수 있고,
  // 드롭다운(복원 대상 선택)도 승인과 동등한 권한이다 — 비승인자가 스냅샷을 바꿔두면 승인자 화면엔
  // 변화가 안 보인 채(type 6) 그 값이 소비된다(설계 §5.4 3겹 방어).
  if (!APPROVERS.includes(userId)) {
    return res(200, { type: 4, data: { content: '⛔ 승인 권한이 없습니다.', flags: 64 } }); // ephemeral
  }

  // 드롭다운: 선택만 저장하고 조용히 확인. 버튼 클릭은 별개 interaction 으로 오고
  // 그 payload 엔 드롭다운 선택값이 실려 오지 않으므로 여기서 눌러 담아야 한다(설계 §3.5).
  if (kind === 'dr-snap') {
    try {
      await ddb.send(
        new UpdateItemCommand({
          TableName: TABLE,
          Key: { approvalId: { S: approvalId } },
          // ⚠️ snapshot 은 DynamoDB 예약어라 표현식에 직접 못 쓴다(항상 ValidationException)
          // → ExpressionAttributeNames 로 우회. 쓰는 쪽(index.py)은 PutItem Item 맵이라 무관.
          UpdateExpression: 'SET #snap = :s',
          ExpressionAttributeNames: { '#snap': 'snapshot' },
          ExpressionAttributeValues: { ':s': { S: body.data.values[0] } },
          // UpdateItem 은 키가 없으면 항목을 만든다 → TTL 만료 후 드롭다운을 건드리면 ttl·taskToken
          // 없는 고아가 생기고, 이후 승인이 "만료" 분기를 건너뛰어 TypeError 로 죽는다.
          ConditionExpression: 'attribute_exists(approvalId)',
        }),
      );
    } catch (e) {
      if (e.name !== 'ConditionalCheckFailedException') throw e;
      return res(200, {
        type: 4,
        data: { content: '⚠️ 만료된 승인 요청입니다(24h TTL).', flags: 64 },
      });
    }
    return res(200, { type: 6 }); // DEFERRED_UPDATE_MESSAGE — 메시지 변화 없음
  }

  if (kind !== 'dr-approve')
    return res(200, { type: 4, data: { content: '알 수 없는 컴포넌트', flags: 64 } });

  const got = await ddb.send(
    new GetItemCommand({
      TableName: TABLE,
      Key: { approvalId: { S: approvalId } },
    }),
  );
  if (!got.Item) {
    return res(200, {
      type: 4,
      data: { content: '⚠️ 만료된 승인 요청입니다(24h TTL).', flags: 64 },
    });
  }

  // 드롭다운을 건드리지 않고 바로 승인했으면 최신 스냅샷으로 폴백.
  const snapshot = got.Item.snapshot?.S ?? got.Item.latestSnapshot.S;

  // ⚠️ **승인은 멱등이어야 하고, 감사 흔적은 첫 승인자로 고정돼야 한다**(codex P2 ×2).
  //
  // 버튼 두 번 클릭 / SendTaskSuccess 가 3초 초과(실측 ~1.6s)로 Discord 재전송 시, 두 번째는
  // 이미 소비된 taskToken 에 응답해 TaskTimedOut·TaskDoesNotExist 를 받는다. 이걸 안 잡으면
  // Lambda 500 → "상호작용 실패" 가 뜨는데 **승인은 실제로 통과**했다(SM 은 다음 단계로 진행).
  //
  // 처음엔 ttl 로 "중복 vs 진짜 만료" 를 갈랐으나 두 결함이 있었다:
  //   · 감사 왜곡: 중복을 현재 클릭자(userId)로 렌더하면, A 승인 직후 B 클릭 시 "B 가 승인" 으로 덮인다.
  //   · 경계 오판: ttl 저장이 RequestApproval 진입보다 **늦다**(approval-request 가 S3 조회 뒤 저장).
  //     24h 경계에서 토큰은 만료됐는데 ttl 은 몇 초 남아, 진짜 만료를 중복으로 오판한다.
  // → **시간 추론을 버리고, 첫 승인 사실을 DynamoDB 에 조건부로 못박는다.**
  //   approvedBy 가 있으면 = 이미 처리된 승인 → 그 값으로 렌더(현재 클릭자 아님).
  //   approvedBy 가 없는데 토큰이 죽었으면 = 아무도 승인 못 한 채 만료 → 진짜 만료.

  const approvedBy = got.Item.approvedBy?.S;
  if (approvedBy) {
    // 이미 누군가 승인함 = 중복 클릭. **저장된 첫 승인자**로 렌더한다(감사 흔적 보존, SendTaskSuccess 재호출 안 함).
    return res(200, disabledMessage(body.message?.content ?? '', approvedBy));
  }

  let recorded = userId; // 렌더에 쓸 승인자 — 아래 조건부 저장이 경합에 지면 저장된 값으로 바뀐다.
  try {
    await sfn.send(
      new SendTaskSuccessCommand({
        taskToken: got.Item.taskToken.S,
        output: JSON.stringify({
          snapshot,
          approvedBy: userId,
          approvedAt: new Date().toISOString(),
        }),
      }),
    );
  } catch (e) {
    if (e.name !== 'TaskTimedOut' && e.name !== 'TaskDoesNotExist') throw e;
    // 토큰이 죽었다. approvedBy 가 없음을 위에서 확인했으니 두 경우다:
    //   (a) 방금 다른 클릭이 SendTaskSuccess 를 먼저 성공시켰다(경합) → 곧 approvedBy 가 찍힌다.
    //   (b) 아무도 승인 못 한 채 RequestApproval 86400s 가 만료됐다 = 진짜 만료.
    // 다시 읽어 approvedBy 유무로 가른다 — ttl 시간 비교(경계 취약)를 쓰지 않는다.
    const re = await ddb.send(
      new GetItemCommand({ TableName: TABLE, Key: { approvalId: { S: approvalId } } }),
    );
    const who = re.Item?.approvedBy?.S;
    if (!who) {
      // (b) 진짜 만료 — 승인 성립 안 함. "만료" 로 정직하게 알린다(성공 표시 금지).
      return res(200, {
        type: 4,
        data: {
          content: '⚠️ 만료된 승인 요청입니다(24h TTL). 페일오버는 시작되지 않았습니다.',
          flags: 64,
        },
      });
    }
    // (a) 경합 — 먼저 성공한 승인자로 렌더한다.
    return res(200, disabledMessage(body.message?.content ?? '', who));
  }

  // SendTaskSuccess 성공 → **첫 승인 사실을 조건부로 못박는다**. attribute_not_exists 로 경합의
  // 늦은 쪽이 값을 덮지 못하게 한다(먼저 저장한 승인자가 감사 흔적의 주인이 된다).
  try {
    await ddb.send(
      new UpdateItemCommand({
        TableName: TABLE,
        Key: { approvalId: { S: approvalId } },
        UpdateExpression: 'SET approvedBy = :u',
        ExpressionAttributeValues: { ':u': { S: userId } },
        ConditionExpression: 'attribute_not_exists(approvedBy)',
      }),
    );
  } catch (e) {
    if (e.name !== 'ConditionalCheckFailedException') throw e;
    // 경합에 졌다 — 다른 승인자가 먼저 못박았다. 그 값으로 렌더한다(SM 은 어차피 진행 중).
    const re = await ddb.send(
      new GetItemCommand({ TableName: TABLE, Key: { approvalId: { S: approvalId } } }),
    );
    recorded = re.Item?.approvedBy?.S ?? userId;
  }

  return res(200, disabledMessage(body.message?.content ?? '', recorded));
};
