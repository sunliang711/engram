variable "image" {
  type    = string
  default = "sunliang711/engram:v0.0.2"
}

job "engram-server" {
  datacenters = ["dc1"]
  type        = "service"

  group "server" {
    count = 1

    network {
      port "http" {
        static = 8977
        to = 8080
      }
    }

    service {
      name = "engram-server"
      provider     = "nomad"
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


    identity {
        name = "vault_default"
        aud  = ["vault.io"]
        ttl  = "1h"
      }

      vault {
        role = "nomad-workloads"
      }
      # Environment values override application defaults; no TOML file is required.
      env {
        ENGRAM_LISTEN_ADDR           = ":8080"
       # ENGRAM_DATABASE_DSN          = var.database_dsn

        # Disable periodic filesystem export; ZIP downloads do not require a volume.
        ENGRAM_EXPORT_ENABLED        = "false"
        ENGRAM_LOG_LEVEL             = "info"
        ENGRAM_WRITE_RATE_PER_MINUTE = "120"
        ENGRAM_COOKIE_SECURE         = "false"
        # Enable only for a deliberate password reset, then disable again.
        RESET_ADMIN                  = "false"
      }

      template {
        data = <<EOH
        # 下面获取nomad中的postgres的host 和 port
      {{- with nomadService "postgres" }}
        {{- with index . 0 }}
          {{- scratch.Set "pg_host" .Address }}
          {{- scratch.Set "pg_port" .Port }}
        {{- end }}
      {{- else }}
        {{- scratch.Set "pg_host" "127.0.0.1" }}
        {{- scratch.Set "pg_port" "5432" }}
      {{- end }}

      {{- with secret "kv/data/app/engram" }}
      #POSTGRES_DSN="host={{ scratch.Get "pg_host" }} user={{ .Data.data.pg_user }} password={{ .Data.data.pg_password }} dbname={{ .Data.data.pg_db }} port={{ scratch.Get "pg_port" }} sslmode=disable"
      ENGRAM_CONTEXT_SIGN_KEY="{{ .Data.data.engram_context_sign_key }}"
      ENGRAM_DATABASE_DSN="postgres://{{ .Data.data.pg_user }}:{{ .Data.data.pg_password}}@{{ scratch.Get "pg_host" }}:{{ scratch.Get "pg_port"}}/engram?sslmode=disable"

      {{- end }}
      EOH

        destination = "local/pg.env"
        env         = true
      }

      config {
        image = var.image
        ports = ["http"]
      }

      resources {
        cpu    = 200
        memory = 50
      }
    }
  }
}
