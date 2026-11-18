job "memory-exporter" {
  datacenters = ["dc1"]
  type        = "batch"

  periodic {
    crons            = ["*/15 * * * *"]
    prohibit_overlap = true
    time_zone        = "UTC"
  }

  group "exporter" {
    task "export-markdown" {
      driver = "docker"

      config {
        image = "ai-memory:v1"
        args  = ["--export-once", "--config", "/etc/ai-memory/config.toml"]
      }

      resources {
        cpu    = 300
        memory = 256
      }
    }
  }
}
