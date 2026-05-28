package executor

import (
	"testing"

	"github.com/requset700k/cledyu/validation-engine/internal/model"
)

func TestNew_KubeVirt(t *testing.T) {
	_, err := New(model.VMSpec{
		Type:      model.VMTypeKubeVirt,
		Name:      "lab-vm-abc123",
		Namespace: "lab-sessions",
	})
	if err != nil {
		t.Errorf("KubeVirt executor 생성 실패: %v", err)
	}
}

func TestNew_KubeVirt_MissingName(t *testing.T) {
	_, err := New(model.VMSpec{
		Type:      model.VMTypeKubeVirt,
		Namespace: "lab-sessions",
	})
	if err == nil {
		t.Error("Name 없으면 에러여야 함")
	}
}

func TestNew_KubeVirt_MissingNamespace(t *testing.T) {
	_, err := New(model.VMSpec{
		Type: model.VMTypeKubeVirt,
		Name: "lab-vm-abc123",
	})
	if err == nil {
		t.Error("Namespace 없으면 에러여야 함")
	}
}

func TestNew_EC2(t *testing.T) {
	_, err := New(model.VMSpec{
		Type:       model.VMTypeEC2,
		InstanceID: "i-0abc1234567890",
		Region:     "ap-northeast-2",
	})
	if err != nil {
		t.Errorf("EC2 executor 생성 실패: %v", err)
	}
}

func TestNew_EC2_MissingInstanceID(t *testing.T) {
	_, err := New(model.VMSpec{
		Type:   model.VMTypeEC2,
		Region: "ap-northeast-2",
	})
	if err == nil {
		t.Error("InstanceID 없으면 에러여야 함")
	}
}

func TestNew_EC2_MissingRegion(t *testing.T) {
	_, err := New(model.VMSpec{
		Type:       model.VMTypeEC2,
		InstanceID: "i-0abc1234567890",
	})
	if err == nil {
		t.Error("Region 없으면 에러여야 함")
	}
}

func TestNew_UnknownType(t *testing.T) {
	_, err := New(model.VMSpec{
		Type: model.VMType("unknown"),
	})
	if err == nil {
		t.Error("알 수 없는 타입은 에러여야 함")
	}
}
