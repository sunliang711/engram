job "memory-server" {
  datacenters = ["dc1"]
  type        = "service"

  group "server" {
    count = 2

    network {
      port "http" {
        to = 8080
      }
    }

    service {
      name = "memory-server"
      port = "http"

      check {
        name     = "liveness"
        type     = "http"
        path     = "/healthz"
        interval = "10s"
        timeout  = "2s"
      }

      check {
        name     = "readiness"
        type     = "http"
        path     = "/readyz"
        interval = "10s"
        timeout  = "3s"
      }
    }

    task "memory-server" {
      driver = "docker"

      config {
        image = "engram:v1"
        ports = ["http"]
        args  = ["/usr/local/bin/memory-server", "--config", "/etc/engram/config.toml"]
        volumes = [
          "/opt/engram/config:/etc/engram:ro",
          "/opt/engram/export:/data/export:rw",
        ]
      }

      resources {
        cpu    = 500
        memory = 512
      }
    }
  }
}
