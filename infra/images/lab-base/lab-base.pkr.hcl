# Ubuntu 22.04 클라우드 이미지를 부팅해 도구를 베이킹하고 qcow2 를 산출한다.
# qemu 빌더는 KVM 호스트(EC2 *.metal)에서 실행된다. user-mode networking 으로
# 아웃바운드만 쓰므로(libguestfs appliance 와 달리) 정상 동작한다.
packer {
  required_plugins {
    qemu = {
      source  = "github.com/hashicorp/qemu"
      version = "~> 1.1"
    }
  }
}

variable "iso_url" {
  type    = string
  default = "https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img"
}

variable "iso_checksum" {
  type    = string
  default = "file:https://cloud-images.ubuntu.com/jammy/current/SHA256SUMS"
}

source "qemu" "lab_base" {
  iso_url          = var.iso_url
  iso_checksum     = var.iso_checksum
  disk_image       = true
  disk_size        = "10G"
  format           = "qcow2"
  accelerator      = "kvm"
  cpus             = 4
  memory           = 4096
  headless         = true
  ssh_username     = "ubuntu"
  ssh_password     = "ubuntu"
  ssh_timeout      = "10m"
  output_directory = "output"
  vm_name          = "lab-base.qcow2"
  # cloud-init seed(NoCloud) 로 ubuntu/ubuntu 로그인 가능하게 한다.
  cd_label = "cidata"
  cd_content = {
    "user-data" = <<-EOF
      #cloud-config
      password: ubuntu
      chpasswd: { expire: false }
      ssh_pwauth: true
      EOF
    "meta-data" = ""
  }
  shutdown_command = "sudo cloud-init clean --logs --seed && sudo shutdown -P now"
}

build {
  sources = ["source.qemu.lab_base"]

  provisioner "shell" {
    execute_command = "sudo -E bash '{{ .Path }}'"
    scripts = [
      "provisioners/00-common.sh",
      "provisioners/10-k3s.sh",
      "provisioners/20-docker.sh",
      "provisioners/30-code-server.sh",
      "provisioners/40-terraform.sh",
      "provisioners/50-ansible-helm.sh",
      "provisioners/99-smoke.sh",
    ]
  }
}
