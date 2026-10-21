// Package config describes the scaler instances: one per GitHub URL and
// scale set, each with its own MIG.
package config

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"
)

// Instance is one scale set served by one MIG.
type Instance struct {
	Name string `mapstructure:"name"`

	URL         string   `mapstructure:"url"`
	ScaleSet    string   `mapstructure:"scale_set"`
	Labels      []string `mapstructure:"labels"`
	RunnerGroup string   `mapstructure:"runner_group"`

	Project      string `mapstructure:"project"`
	Region       string `mapstructure:"region"`
	MIG          string `mapstructure:"mig"`
	RunnerPrefix string `mapstructure:"runner_prefix"`
	WorkFolder   string `mapstructure:"work_folder"`

	MinRunners int `mapstructure:"min_runners"`
	MaxRunners int `mapstructure:"max_runners"`

	ReconcileInterval time.Duration `mapstructure:"reconcile_interval"`
	BootTimeout       time.Duration `mapstructure:"boot_timeout"`
	IdleTimeout       time.Duration `mapstructure:"idle_timeout"`
	MaxRunnerAge      time.Duration `mapstructure:"max_runner_age"`

	// Optional overrides of the shared credentials: another GitHub App
	// installation (another org), or a PAT read from this env var.
	AppInstallationID int64  `mapstructure:"app_installation_id"`
	GitHubTokenEnv    string `mapstructure:"github_token_env"`
}

// Load reads the instances of a TOML file. Each one is base (the flags),
// then [defaults], then its own fields.
func Load(path string, base Instance) ([]Instance, error) {
	v := viper.New()
	v.SetConfigFile(path)
	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	for key := range v.AllSettings() {
		if key != "defaults" && key != "instances" {
			return nil, fmt.Errorf("%s: unknown section %q, want [defaults] and [[instances]]", path, key)
		}
	}

	defaults := base.clone()
	if raw := v.Get("defaults"); raw != nil {
		if err := decode(raw, &defaults); err != nil {
			return nil, fmt.Errorf("%s [defaults]: %w", path, err)
		}
	}

	raws, ok := v.Get("instances").([]any)
	if !ok || len(raws) == 0 {
		return nil, fmt.Errorf("%s: no [[instances]]", path)
	}

	instances := make([]Instance, 0, len(raws))
	for i, raw := range raws {
		inst := defaults.clone()
		if err := decode(raw, &inst); err != nil {
			return nil, fmt.Errorf(
				"%s instance %d: %w",
				path,
				i+1,
				err,
			)
		}
		instances = append(instances, inst)
	}

	return instances, Validate(instances)
}

// decode applies the keys present in raw on top of inst. Unknown keys are
// errors, so a typo fails instead of being ignored.
func decode(raw any, inst *Instance) error {
	fields, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("want a table, got %T", raw)
	}
	// A list given here replaces the inherited one.
	if _, ok := fields["labels"]; ok {
		inst.Labels = nil
	}

	decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		Result:      inst,
		ErrorUnused: true,
		DecodeHook:  mapstructure.StringToTimeDurationHookFunc(),
	})
	if err != nil {
		return err
	}
	return decoder.Decode(fields)
}

// Validate checks each instance, and that no two instances share a name, a
// scale set, or a MIG with the same runner prefix.
func Validate(instances []Instance) error {
	type scaleSetKey struct{ url, group, name string }
	type migKey struct{ project, region, mig, prefix string }

	names := newOwners[string]("have the same name")
	scaleSets := newOwners[scaleSetKey]("use the same scale set")
	migs := newOwners[migKey]("share a MIG and runner prefix")
	errs := []error{}

	for _, inst := range instances {
		if err := inst.validate(); err != nil {
			errs = append(errs, fmt.Errorf("instance %q: %w", inst.Name, err))
			continue
		}

		scaleSet := scaleSetKey{url: inst.URL, group: inst.RunnerGroup, name: inst.ScaleSet}
		mig := migKey{project: inst.Project, region: inst.Region, mig: inst.MIG, prefix: inst.RunnerPrefix}

		errs = append(
			errs,
			names.claim(inst.Name, inst.Name),
			scaleSets.claim(scaleSet, inst.Name),
			migs.claim(mig, inst.Name),
		)
	}

	return errors.Join(errs...)
}

// owners maps a resource to the instance using it.
type owners[K comparable] struct {
	byKey map[K]string
	clash string
}

func newOwners[K comparable](clash string) owners[K] {
	return owners[K]{byKey: map[K]string{}, clash: clash}
}

// claim gives key to instance name, or fails when another instance has it.
func (o owners[K]) claim(key K, name string) error {
	if other, ok := o.byKey[key]; ok {
		return fmt.Errorf(
			"instances %q and %q %s",
			other,
			name,
			o.clash,
		)
	}
	o.byKey[key] = name
	return nil
}

func (inst Instance) validate() error {
	required := map[string]string{
		"name":          inst.Name,
		"url":           inst.URL,
		"scale_set":     inst.ScaleSet,
		"project":       inst.Project,
		"region":        inst.Region,
		"mig":           inst.MIG,
		"runner_prefix": inst.RunnerPrefix,
	}

	missing := []string{}
	for field, value := range required {
		if value == "" {
			missing = append(missing, field)
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return fmt.Errorf("missing %v", missing)
	}

	minInRange := inst.MinRunners >= 0 && inst.MinRunners <= inst.MaxRunners
	if inst.MaxRunners < 1 || !minInRange {
		return fmt.Errorf(
			"need 0 <= min_runners (%d) <= max_runners (%d), max at least 1",
			inst.MinRunners,
			inst.MaxRunners,
		)
	}

	return inst.validateDurations()
}

const minReconcileInterval = 10 * time.Second

func (inst Instance) validateDurations() error {
	if inst.ReconcileInterval < minReconcileInterval {
		return fmt.Errorf("reconcile_interval %v is below %v", inst.ReconcileInterval, minReconcileInterval)
	}

	durations := map[string]time.Duration{
		"boot_timeout":   inst.BootTimeout,
		"idle_timeout":   inst.IdleTimeout,
		"max_runner_age": inst.MaxRunnerAge,
	}
	for name, d := range durations {
		if d <= 0 {
			return fmt.Errorf("%s must be positive, got %v", name, d)
		}
	}

	if inst.MaxRunnerAge <= inst.IdleTimeout {
		return fmt.Errorf("max_runner_age %v must be above idle_timeout %v", inst.MaxRunnerAge, inst.IdleTimeout)
	}
	return nil
}

func (inst Instance) clone() Instance {
	inst.Labels = slices.Clone(inst.Labels)
	return inst
}
