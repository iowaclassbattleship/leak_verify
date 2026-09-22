package app

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// configFile is the on-disk shape of config.yaml.
//
//	users:
//	  marc@northward.ch: a-password
//	  someone@who-umc.org: another-password
type configFile struct {
	Master string            `yaml:"master"`
	Secret string            `yaml:"secret"`
	Users  map[string]string `yaml:"users"`
}

// Settings is everything config.yaml carries.
type Settings struct {
	Master string
	Secret string
	Users  Users
}

// Users maps a lowercased email address to its password.
type Users map[string]string

// LoadUsers reads the sign-in list. Addresses are lowercased and trimmed, so
// the capitalisation someone types at the login screen does not matter.
func LoadUsers(path string) (Settings, error) {
	var out Settings
	raw, err := os.ReadFile(path)
	if err != nil {
		return out, fmt.Errorf("reading %s: %w", path, err)
	}
	var f configFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return out, fmt.Errorf("parsing %s: %w", path, err)
	}
	if strings.TrimSpace(f.Master) == "" {
		return out, fmt.Errorf("%s: no master password set, which /debug/log needs", path)
	}
	if strings.TrimSpace(f.Secret) == "" {
		return out, fmt.Errorf("%s: no secret set, which derives each user's marking key", path)
	}
	out.Master = f.Master
	out.Secret = f.Secret
	users := Users{}
	for email, password := range f.Users {
		email = strings.ToLower(strings.TrimSpace(email))
		switch {
		case email == "":
			return out, fmt.Errorf("%s: a user has no email address", path)
		case !strings.Contains(email, "@"):
			return out, fmt.Errorf("%s: %q is not an email address", path, email)
		case password == "":
			return out, fmt.Errorf("%s: %s has no password", path, email)
		}
		if _, dup := users[email]; dup {
			return out, fmt.Errorf("%s: %s is listed twice", path, email)
		}
		users[email] = password
	}
	if len(users) == 0 {
		return out, fmt.Errorf("%s: no users listed", path)
	}
	out.Users = users
	return out, nil
}
