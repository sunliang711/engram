job "memory-backup" {
  datacenters = ["dc1"]
  type        = "batch"

  periodic {
    crons            = ["0 2 * * *"]
    prohibit_overlap = true
    time_zone        = "UTC"
  }

  group "backup" {
    task "pg-backup" {
      driver = "docker"

      config {
        image = "postgres:16-alpine"
        args  = ["/bin/sh", "/scripts/backup.sh"]
      }

      resources {
        cpu    = 300
        memory = 512
      }
    }
  }
}
