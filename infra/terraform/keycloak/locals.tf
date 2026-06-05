locals {
  # 운영자(내부) realm 역할만 둔다. student/instructor 학습자 역할은
  # cledyu-learn realm(roles-learn.tf)으로 분리되었다 — 운영 권한과 학습자 격리.
  realm_roles = {
    admin = {
      description = "Operational administrator role."
    }
    observer = {
      description = "Read-only observer role for dashboards and logs."
    }
    kafka-admin = {
      description = "Full access to Kafka UI (topics, consumers, schemas, ACL)."
    }
    kafka-viewer = {
      description = "Read-only access to Kafka UI (topic view, consumer view)."
    }
  }

  groups = {
    team-platform = {
      realm_roles = ["admin", "kafka-admin"]
    }
    team-security = {
      realm_roles = ["admin", "kafka-viewer"]
    }
    team-observability = {
      realm_roles = ["admin", "observer", "kafka-viewer"]
    }
    team-lab-data = {
      realm_roles = ["admin", "observer", "kafka-admin"]
    }
    team-ai = {
      realm_roles = ["admin", "observer", "kafka-viewer"]
    }
    team-service = {
      realm_roles = ["admin", "observer", "kafka-viewer"]
    }
  }

  confidential_client_ids = [
    for client_id, client in var.oidc_clients : client_id
    if upper(client.access_type) == "CONFIDENTIAL"
  ]
}
