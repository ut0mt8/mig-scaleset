package ghclient

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	cases := map[string]struct {
		auth Auth
		want string
	}{
		"token":           {auth: Auth{Token: "t"}},
		"complete app":    {auth: Auth{AppID: "c", AppInstallationID: 1, AppPrivateKey: "k"}},
		"missing install": {auth: Auth{AppID: "c"}, want: "both"},
		"missing client":  {auth: Auth{AppInstallationID: 1}, want: "both"},
		"two private keys": {
			auth: Auth{AppID: "c", AppInstallationID: 1, AppPrivateKey: "k", AppPrivateKeyFile: "f"},
			want: "not both",
		},
	}

	for name, tc := range cases {
		err := tc.auth.validate()
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: unexpected error %v", name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf(
				"%s: got %v, want an error containing %q",
				name,
				err,
				tc.want,
			)
		}
	}
}

func TestPrivateKey(t *testing.T) {
	pem := "-----BEGIN RSA PRIVATE KEY-----\nabc\n-----END RSA PRIVATE KEY-----"
	encoded := base64.StdEncoding.EncodeToString([]byte(pem))

	for name, value := range map[string]string{"pem": pem, "base64": encoded} {
		got, err := Auth{AppPrivateKey: value}.privateKey()
		if err != nil || got != pem {
			t.Errorf("%s: got %q, %v", name, got, err)
		}
	}

	if _, err := (Auth{AppPrivateKey: "not a key"}).privateKey(); err == nil {
		t.Error("expected an error for a key that is neither PEM nor base64")
	}
}
