module hctl

go 1.25.0

require (
	github.com/gofrs/flock v0.13.0
	github.com/pelletier/go-toml/v2 v2.2.4
	github.com/robfig/cron/v3 v3.0.1
	go.yaml.in/yaml/v3 v3.0.5
	golang.org/x/sys v0.37.0
	hctl/channeladapter v0.0.0
)

replace hctl/channeladapter => ./channeladapter
