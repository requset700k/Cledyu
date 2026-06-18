package kubevirt

import (
	"fmt"
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
	// 검증엔진(virtctl ssh)이 키로 접속할 수 있도록 lab 사용자에 엔진 공개키를 넣는다.
	// 공개키 미설정 시 비번/시리얼 콘솔만 유지(키 블록 생략).
	sshKeyBlock := ""
	if labSSHPublicKey != "" {
		sshKeyBlock = "\n    ssh_authorized_keys:\n      - " + labSSHPublicKey
	}

	var b strings.Builder
	fmt.Fprintf(&b, `#cloud-config
hostname: %s
ssh_pwauth: true
users:
  - name: lab
    lock_passwd: false
    shell: /bin/bash
    sudo: ALL=(ALL) NOPASSWD:ALL%s
chpasswd:
  expire: false
  list: |
    lab:lab
bootcmd:
  - "mkdir -p /etc/systemd/resolved.conf.d"
  - "printf '%%s\\n' '[Resolve]' 'DNS=8.8.8.8 1.1.1.1' 'FallbackDNS=8.8.4.4 1.0.0.1' 'Domains=~.' > /etc/systemd/resolved.conf.d/cledyu-lab.conf"
  - "systemctl restart systemd-resolved || true"
  - "systemctl stop serial-getty@ttyS0.service || true"
`, "session-"+sessionID, sshKeyBlock)

	if len(init.Packages) > 0 {
		b.WriteString("packages:\n")
		for _, p := range init.Packages {
			fmt.Fprintf(&b, "  - %s\n", p)
		}
	}

	b.WriteString(`write_files:
  - path: /etc/systemd/system/serial-getty@ttyS0.service.d/override.conf
    permissions: "0644"
    content: |
      [Service]
      ExecStart=
      ExecStart=-/sbin/agetty --autologin lab --noclear --keep-baud 115200,38400,9600 ttyS0 xterm-256color
  - path: /home/lab/.hushlogin
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
      # Clear the KubeVirt serial-console boot scrollback before showing the lab prompt.
      printf '\033[H\033[2J\033[3J'
      cd /home/lab
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
