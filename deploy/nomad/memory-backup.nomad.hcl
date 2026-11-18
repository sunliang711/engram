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

      env {
        BACKUP_DIR          = "/backups"
        ENGRAM_DATABASE_DSN = "postgres://postgres:postgres@postgres.service.consul:5432/engram?sslmode=disable"
      }

      config {
        image   = "postgres:16-alpine"
        command = "/bin/sh"
        args    = ["/scripts/backup.sh"]
        volumes = [
          "/opt/engram/scripts:/scripts:ro",
          "/opt/engram/backups:/backups:rw",
        ]
      }

      resources {
        cpu    = 300
        memory = 512
      }
    }
  }
}
