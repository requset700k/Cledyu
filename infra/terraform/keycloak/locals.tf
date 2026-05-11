locals {
  realm_roles = {
    student = {
      description = "Learner role for lab access and AI tutor usage."
    }
    instructor = {
      description = "Instructor role for learner creation and instructor mode."
    }
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
    students-cohort-0 = {
      realm_roles = ["student"]
    }
    instructors = {
      realm_roles = ["instructor"]
    }
  }

  confidential_client_ids = [
    for client_id, client in var.oidc_clients : client_id
    if upper(client.access_type) == "CONFIDENTIAL"
  ]
}
