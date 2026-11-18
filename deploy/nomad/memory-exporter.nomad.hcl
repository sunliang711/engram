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
        ENGRAM_EXPORT_DIR = "/data/export"
      }

      config {
        image = "engram:v1"
        args  = ["--export-once", "--config", "/etc/engram/config.toml"]
        volumes = [
          "/opt/engram/config:/etc/engram:ro",
          "/opt/engram/export:/data/export:rw",
        ]
      }

      resources {
        cpu    = 300
        memory = 256
      }
    }
  }
}
