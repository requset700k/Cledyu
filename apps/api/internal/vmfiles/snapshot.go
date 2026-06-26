// Package vmfiles는 실습 VM 내부 파일을 제한된 읽기 전용 목록으로 제공한다.
package vmfiles

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"unicode"
)

const (
	// RootPath는 의도적으로 고정한다. 공개 API는 임의의 VM 경로를 입력받지 않는다.
	RootPath = "/home/lab"
	// MaxDepth는 VM 내부 도우미가 재귀 탐색할 수 있는 최대 깊이다.
	MaxDepth = 4
	// MaxEntries는 큰 디렉터리가 API나 브라우저 메모리를 고갈시키지 않도록 제한한다.
	MaxEntries = 500
	// MaxPayloadBytes는 VM 내부 도우미가 변경되거나 침해된 경우에도 적용되는 2차 한도다.
	MaxPayloadBytes = 256 * 1024
	// MaxReadPayloadBytes는 read preview JSON의 전송 한도다. VM helper는 raw 128KB를
	// json.dump 기본값(ensure_ascii=true)으로 인코딩하므로 비ASCII 텍스트는 최대 6배 팽창할 수 있다.
	MaxReadPayloadBytes = 1024 * 1024
)

// Entry는 RootPath 아래에서 표시할 일반 파일 또는 디렉터리 하나다.
type Entry struct {
	Path  string `json:"path"`
	Name  string `json:"name"`
	Type  string `json:"type"`
	Depth int    `json:"depth"`
}

// Snapshot은 Web 클라이언트에 반환하는 제한된 전체 트리다.
type Snapshot struct {
	Root      string  `json:"root"`
	Items     []Entry `json:"items"`
	Truncated bool    `json:"truncated"`
}

// ParseSnapshot은 VM 출력이 Session API 신뢰 경계를 통과하기 전에 검증한다.
// VM 내부 도우미도 같은 제한을 적용하지만, 학습자가 자신의 VM을 변경할 수 있으므로
// API가 VM 출력을 암묵적으로 신뢰하지 않고 한 번 더 검증한다.
func ParseSnapshot(raw []byte) (Snapshot, error) {
	if len(raw) > MaxPayloadBytes {
		return Snapshot{}, fmt.Errorf("file snapshot exceeds %d bytes", MaxPayloadBytes)
	}

	var snapshot Snapshot
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("decode file snapshot: %w", err)
	}
	if err := ensureJSONEOF(dec); err != nil {
		return Snapshot{}, err
	}
	// 누락되거나 null인 items도 Web 계약에서는 항상 빈 JSON 배열로 반환한다.
	if snapshot.Items == nil {
		snapshot.Items = []Entry{}
	}
	if snapshot.Root != RootPath {
		return Snapshot{}, fmt.Errorf("unexpected file snapshot root %q", snapshot.Root)
	}
	if len(snapshot.Items) > MaxEntries {
		return Snapshot{}, fmt.Errorf("file snapshot contains %d entries; maximum is %d", len(snapshot.Items), MaxEntries)
	}
	for i := range snapshot.Items {
		if err := validateEntry(snapshot.Items[i]); err != nil {
			return Snapshot{}, fmt.Errorf("invalid file snapshot item %d: %w", i, err)
		}
	}

	// VM 파일시스템 순회 결과가 환경에 따라 달라도 API 응답과 Web 렌더링 순서는
	// 항상 같도록 경로 기준으로 정규화한다.
	sort.Slice(snapshot.Items, func(i, j int) bool {
		return snapshot.Items[i].Path < snapshot.Items[j].Path
	})
	return snapshot, nil
}

func ensureJSONEOF(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); err == nil {
		return errors.New("decode file snapshot: multiple JSON values")
	} else if !errors.Is(err, io.EOF) {
		return fmt.Errorf("decode file snapshot trailer: %w", err)
	}
	return nil
}

func validateEntry(entry Entry) error {
	if entry.Type != "file" && entry.Type != "directory" {
		return fmt.Errorf("unsupported type %q", entry.Type)
	}
	segments, err := validateRelativePath(entry.Path)
	if err != nil {
		return err
	}
	if len(segments) > MaxDepth || entry.Depth != len(segments) {
		return fmt.Errorf("depth %d does not match bounded path %q", entry.Depth, entry.Path)
	}
	if entry.Name != path.Base(entry.Path) {
		return fmt.Errorf("name %q does not match path %q", entry.Name, entry.Path)
	}
	return nil
}

func validateRelativePath(relativePath string) ([]string, error) {
	if relativePath == "" || path.IsAbs(relativePath) || path.Clean(relativePath) != relativePath {
		return nil, fmt.Errorf("unsafe relative path %q", relativePath)
	}
	if strings.ContainsRune(relativePath, '\\') {
		return nil, fmt.Errorf("unsafe relative path %q", relativePath)
	}
	segments := strings.Split(relativePath, "/")
	for _, segment := range segments {
		if segment == "" || strings.HasPrefix(segment, ".") || strings.IndexFunc(segment, unicode.IsControl) >= 0 {
			return nil, fmt.Errorf("hidden or unsafe path segment %q", segment)
		}
	}
	return segments, nil
}
