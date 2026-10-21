// Package settings reads flags and their MIG_SCALER_* env vars with viper:
// command line first, then env var, then flag default.
package settings

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cast"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

const envPrefix = "MIG_SCALER_"

// EnvName maps a flag to its env var: --max-runners is MIG_SCALER_MAX_RUNNERS.
func EnvName(flag string) string {
	return envPrefix + strings.ToUpper(strings.ReplaceAll(flag, "-", "_"))
}

// Options tune how flags map to env vars.
type Options struct {
	// Flags only read from the command line.
	Skip []string
	// Extra env var read for a flag when its MIG_SCALER_ one is not set.
	Aliases map[string]string
}

func (o Options) envNames(flag string) []string {
	names := []string{EnvName(flag)}
	if alias, ok := o.Aliases[flag]; ok {
		names = append(names, alias)
	}
	return names
}

// Document adds the env var names to each flag's help text.
func Document(flags *pflag.FlagSet, opts Options) {
	flags.VisitAll(func(f *pflag.Flag) {
		if slices.Contains(opts.Skip, f.Name) {
			return
		}
		f.Usage = fmt.Sprintf("%s [$%s]", f.Usage, strings.Join(opts.envNames(f.Name), ", $"))
	})
}

// Reader reads settings and collects invalid values, so every mistake is
// reported at once instead of silently becoming a zero.
type Reader struct {
	v    *viper.Viper
	errs []error
}

// Bind binds every flag and its env vars.
func Bind(flags *pflag.FlagSet, opts Options) (*Reader, error) {
	v := viper.New()
	errs := []error{}

	flags.VisitAll(func(f *pflag.Flag) {
		if slices.Contains(opts.Skip, f.Name) {
			return
		}
		if err := v.BindPFlag(f.Name, f); err != nil {
			errs = append(errs, err)
		}
		env := append([]string{f.Name}, opts.envNames(f.Name)...)
		if err := v.BindEnv(env...); err != nil {
			errs = append(errs, err)
		}
	})

	return &Reader{v: v, errs: []error{}}, errors.Join(errs...)
}

func (r *Reader) String(key string) string {
	return r.v.GetString(key)
}

func (r *Reader) Int(key string) int {
	n, err := cast.ToIntE(r.v.Get(key))
	r.check(key, err)
	return n
}

func (r *Reader) Int64(key string) int64 {
	n, err := cast.ToInt64E(r.v.Get(key))
	r.check(key, err)
	return n
}

func (r *Reader) Duration(key string) time.Duration {
	d, err := cast.ToDurationE(r.v.Get(key))
	r.check(key, err)
	return d
}

// Strings reads a list. Env vars are comma separated.
func (r *Reader) Strings(key string) []string {
	items := []string{}
	for _, item := range r.v.GetStringSlice(key) {
		for _, part := range strings.Split(item, ",") {
			if part = strings.TrimSpace(part); part != "" {
				items = append(items, part)
			}
		}
	}
	return items
}

// Err returns every invalid value read so far.
func (r *Reader) Err() error {
	return errors.Join(r.errs...)
}

func (r *Reader) check(key string, err error) {
	if err == nil {
		return
	}
	r.errs = append(r.errs, fmt.Errorf(
		"--%s / $%s: %w",
		key,
		EnvName(key),
		err,
	))
}
