package connector

import (
	_ "embed"

	"go.mau.fi/util/configupgrade"
)

//go:embed example-config.yaml
var ExampleConfig string

type Config struct {
	DisablePollPolling bool `yaml:"disable_poll_polling"`
}

func upgradeConfig(helper configupgrade.Helper) {
	helper.Copy(configupgrade.Bool, "disable_poll_polling")
}
