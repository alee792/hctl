module hctl

go 1.24.0

require (
	github.com/bwmarrin/discordgo v0.29.0
	github.com/gofrs/flock v0.13.0
	github.com/pelletier/go-toml/v2 v2.2.4
	github.com/robfig/cron/v3 v3.0.1
	github.com/zalando/go-keyring v0.2.6
	go.yaml.in/yaml/v3 v3.0.5
	golang.org/x/sys v0.37.0
	hctl/channeladapter v0.0.0
)

replace hctl/channeladapter => ./channeladapter

require (
	al.essio.dev/pkg/shellescape v1.5.1 // indirect
	github.com/danieljoos/wincred v1.2.2 // indirect
	github.com/godbus/dbus/v5 v5.1.0 // indirect
	github.com/gorilla/websocket v1.4.2 // indirect
	golang.org/x/crypto v0.0.0-20210421170649-83a5a9bb288b // indirect
)
