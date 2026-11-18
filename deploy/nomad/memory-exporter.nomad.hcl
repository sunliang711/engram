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

      env {
        MEMORY_EXPORT_DIR = "/data/export"
      }

      config {
        image = "ai-memory:v1"
        args  = ["--export-once", "--config", "/etc/ai-memory/config.toml"]
        volumes = [
          "/opt/ai-memory/config:/etc/ai-memory:ro",
          "/opt/ai-memory/export:/data/export:rw",
        ]
      }

      resources {
        cpu    = 300
        memory = 256
      }
    }
  }
}
