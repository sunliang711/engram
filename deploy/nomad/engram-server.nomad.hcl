variable "image" {
  type    = string
  default = "sunliang711/engram:latest"
}

variable "database_dsn" {
  type = string
}

variable "context_sign_key" {
  type = string
}

job "engram-server" {
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
      name = "engram-server"
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

    task "engram-server" {
      driver = "docker"

      # Environment values override application defaults; no TOML file is required.
      env {
        ENGRAM_LISTEN_ADDR           = ":8080"
        ENGRAM_DATABASE_DSN          = var.database_dsn
        # All replicas must share one persistent signing key of at least 32 bytes.
        ENGRAM_CONTEXT_SIGN_KEY      = var.context_sign_key
        # Disable periodic filesystem export; ZIP downloads do not require a volume.
        ENGRAM_EXPORT_ENABLED        = "false"
        ENGRAM_LOG_LEVEL             = "info"
        ENGRAM_WRITE_RATE_PER_MINUTE = "120"
        ENGRAM_COOKIE_SECURE         = "false"
        # Enable only for a deliberate password reset, then disable again.
        RESET_ADMIN                  = "false"
      }

      config {
        image = var.image
        ports = ["http"]
      }

      resources {
        cpu    = 500
        memory = 512
      }
    }
  }
}
