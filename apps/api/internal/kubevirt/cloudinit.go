package kubevirt

import (
	"fmt"
	"strconv"
	"strings"
)

// renderCloudInit은 세션 VM 의 #cloud-config userdata 를 만든다.
//
// 공통부: lab 사용자(autologin·sudo) + DNS upstream 보정 + serial getty 오버라이드.
// 랩별부(init): packages(apt) 와 runcmd 추가 — Lab DSL 의 init 필드에서 온다.
// DNS 는 packages 모듈보다 먼저 필요하므로 runcmd 가 아니라 bootcmd 에서 적용한다.
// init.Runcmd 는 공통 runcmd(getty 재시작) 앞에 붙어, 도구 설치가 끝난 뒤
// 학생 터미널 autologin 이 열리는 순서를 보장한다.
func renderCloudInit(sessionID, labSSHPublicKey string, init BootInit) string {
	return renderCloudInitWithAccess(sessionID, labSSHPublicKey, "", init)
}

// renderCloudInitWithAccess는 검증엔진 key와 파일 목록 전용 제한 key를 함께 렌더링한다.
// 파일 목록 public key가 비어 있으면 관련 key와 강제 명령을 모두 생략한다.
func renderCloudInitWithAccess(
	sessionID, labSSHPublicKey, fileListSSHPublicKey string,
	init BootInit,
) string {
	var b strings.Builder
	fmt.Fprintf(&b, `#cloud-config
hostname: %s
ssh_pwauth: true
users:
  - name: lab
    lock_passwd: false
    shell: /bin/bash
    sudo: ALL=(ALL) NOPASSWD:ALL
`, "session-"+sessionID)

	var sshKeys []string
	if key := strings.TrimSpace(labSSHPublicKey); key != "" {
		sshKeys = append(sshKeys, key)
	}
	if key := strings.TrimSpace(fileListSSHPublicKey); key != "" {
		// Session API 파일 목록 key는 VM 안에서 범용 셸이 아니라 고정된 JSON 출력 명령만 실행한다.
		sshKeys = append(sshKeys, `command="/usr/local/libexec/cledyu-list-files",restrict `+key)
	}
	if len(sshKeys) > 0 {
		b.WriteString("    ssh_authorized_keys:\n")
		for _, key := range sshKeys {
			// strconv.Quote의 double-quoted scalar는 key 옵션 내부 따옴표도 YAML-safe하게 보존한다.
			fmt.Fprintf(&b, "      - %s\n", strconv.Quote(key))
		}
	}

	b.WriteString(`chpasswd:
  expire: false
  list: |
    lab:lab
bootcmd:
  - "mkdir -p /etc/systemd/resolved.conf.d"
  - "printf '%%s\\n' '[Resolve]' 'DNS=8.8.8.8 1.1.1.1' 'FallbackDNS=8.8.4.4 1.0.0.1' 'Domains=~.' > /etc/systemd/resolved.conf.d/cledyu-lab.conf"
  - "systemctl restart systemd-resolved || true"
  - "systemctl stop serial-getty@ttyS0.service || true"
`)

	if len(init.Packages) > 0 {
		b.WriteString("packages:\n")
		for _, p := range init.Packages {
			fmt.Fprintf(&b, "  - %s\n", p)
		}
	}

	// write_files 모듈은 보통 users-groups 모듈보다 먼저 실행되므로, lab 계정을
	// owner로 쓰는 항목은 defer: true 로 final stage(유저 생성 이후)로 미뤄야 한다.
	// 안 그러면 "Unknown user or group: lab" 으로 파일 생성이 실패한다.
	b.WriteString(`write_files:
  - path: /etc/systemd/system/serial-getty@ttyS0.service.d/override.conf
    permissions: "0644"
    content: |
      [Service]
      ExecStart=
      ExecStart=-/sbin/agetty --autologin lab --noclear --keep-baud 115200,38400,9600 ttyS0 xterm-256color
`)
	if strings.TrimSpace(fileListSSHPublicKey) != "" {
		b.WriteString(`  - path: /usr/local/libexec/cledyu-list-files
    owner: root:root
    permissions: "0755"
    content: |
      #!/usr/bin/env python3
      import json
      import os
      import sys

      ROOT = "/home/lab"
      MAX_DEPTH = 4
      MAX_ENTRIES = 500
      MAX_READ_BYTES = 131072
      items = []
      truncated = False

      def fail(message, code=64):
          sys.stderr.write(message + "\n")
          raise SystemExit(code)

      def normalize_relative(relative_path):
          if not relative_path:
              fail("path is required")
          normalized = os.path.normpath(relative_path)
          if (
              normalized == "."
              or os.path.isabs(normalized)
              or normalized.startswith("../")
              or normalized == ".."
              or "\\" in normalized
              or "\x00" in normalized
          ):
              fail("unsafe path")
          for segment in normalized.split(os.sep):
              if not segment or segment.startswith("."):
                  fail("hidden or unsafe path")
          return normalized

      def resolve_under_root(relative_path):
          normalized = normalize_relative(relative_path)
          root_real = os.path.realpath(ROOT)
          target = os.path.realpath(os.path.join(ROOT, normalized))
          if target != root_real and not target.startswith(root_real + os.sep):
              fail("path escapes root")
          return normalized, target

      def walk(directory, relative, depth):
          global truncated
          try:
              entries = sorted(os.scandir(directory), key=lambda entry: entry.name)
          except OSError:
              return
          for entry in entries:
              if entry.name.startswith(".") or entry.is_symlink():
                  continue
              child_relative = f"{relative}/{entry.name}" if relative else entry.name
              try:
                  if entry.is_dir(follow_symlinks=False):
                      entry_type = "directory"
                  elif entry.is_file(follow_symlinks=False):
                      entry_type = "file"
                  else:
                      continue
              except OSError:
                  continue
              if len(items) >= MAX_ENTRIES:
                  truncated = True
                  return
              items.append({
                  "path": child_relative,
                  "name": entry.name,
                  "type": entry_type,
                  "depth": depth,
              })
              if entry_type == "directory" and depth < MAX_DEPTH:
                  walk(entry.path, child_relative, depth + 1)
                  if truncated:
                      return

      def list_files():
          walk(ROOT, "", 1)
          json.dump({"root": ROOT, "items": items, "truncated": truncated}, sys.stdout)
          sys.stdout.write("\n")

      def read_file(relative_path):
          normalized, target = resolve_under_root(relative_path)
          if os.path.islink(target) or not os.path.isfile(target):
              fail("not a regular file", 66)
          with open(target, "rb") as handle:
              data = handle.read(MAX_READ_BYTES + 1)
          truncated = len(data) > MAX_READ_BYTES
          if truncated:
              data = data[:MAX_READ_BYTES]
          try:
              content = data.decode("utf-8")
          except UnicodeDecodeError:
              fail("binary file preview is unavailable", 65)
          json.dump({"path": normalized, "content": content, "truncated": truncated}, sys.stdout)
          sys.stdout.write("\n")

      command = os.environ.get("SSH_ORIGINAL_COMMAND", "").strip() or "list"
      action, _, argument = command.partition(" ")
      if action == "list" and not argument:
          list_files()
      elif action == "read" and argument:
          read_file(argument.strip())
      else:
          fail("unsupported command")
`)
	}
	b.WriteString(`  - path: /home/lab/.hushlogin
    owner: lab:lab
    permissions: "0644"
    defer: true
    content: ""
  - path: /home/lab/.bash_profile
    owner: lab:lab
    permissions: "0644"
    defer: true
    content: |
      if [ -f /home/lab/.bashrc ]; then
        . /home/lab/.bashrc
      fi
      # 부팅 시 보이던 cloud-init/시리얼 콘솔 스크롤백을 지운다 (Clear the KubeVirt
      # serial-console boot scrollback before showing the lab prompt).
      printf '\033[H\033[2J\033[3J'
      cd /home/lab
  - path: /home/lab/.bashrc
    owner: lab:lab
    permissions: "0644"
    defer: true
    append: true
    content: |
      # 학습자 화면에는 내부 session hostname 대신 Cledyu 라벨만 보여준다.
      PS1='\[\033[1;32m\]Cledyu\[\033[0m\] \w ➜ '
runcmd:
`)

	// 랩별 초기화 — 한 줄 셸 명령을 YAML 단일 스칼라(따옴표 string)로 넣는다.
	// %q 가 셸 명령 내부의 " 를 이스케이프해 YAML 파싱 안전성을 보장한다.
	for _, cmd := range init.Runcmd {
		fmt.Fprintf(&b, "  - %q\n", cmd)
	}
	b.WriteString(`  - systemctl daemon-reload
  - systemctl restart serial-getty@ttyS0.service
`)
	return b.String()
}
