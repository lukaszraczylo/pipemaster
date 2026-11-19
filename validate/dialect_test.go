package validate_test

import (
	"strings"
	"testing"

	_ "github.com/lukaszraczylo/pipemaster/provider/gha"
	"github.com/lukaszraczylo/pipemaster/validate"
)

func wf(top, permissions string) []byte {
	return []byte("on: push\n" + top + permissions + "jobs:\n  a:\n    runs-on: r\n    steps:\n      - run: echo hi\n")
}

func TestValidateDialect(t *testing.T) {
	codeRead := "permissions:\n  code: read\n"
	cases := []struct {
		name, id string
		wantErr  string
		data     []byte
	}{
		{name: "gitea accepts code scope", id: "gitea", data: wf("", codeRead), wantErr: ""},
		{name: "gha rejects code scope", id: "gha", data: wf("", codeRead), wantErr: "code"},
		{name: "forgejo rejects gitea-only scope", id: "forgejo", data: wf("", codeRead), wantErr: "code"},
		{name: "forgejo accepts openid-connect", id: "forgejo", data: wf("enable-openid-connect: true\n", ""), wantErr: ""},
		{name: "gitea rejects openid-connect", id: "gitea", data: wf("enable-openid-connect: true\n", ""), wantErr: "enable-openid-connect"},
		{name: "gha rejects openid-connect", id: "gha", data: wf("enable-openid-connect: true\n", ""), wantErr: "enable-openid-connect"},
		{name: "forgejo job openid-connect", id: "forgejo", data: []byte("on: push\njobs:\n  a:\n    runs-on: r\n    enable-openid-connect: true\n    steps:\n      - run: echo\n"), wantErr: ""},
		{name: "gitea rejects gha-only scope", id: "gitea", data: wf("", "permissions:\n  checks: read\n"), wantErr: "checks"},
		{name: "gitea accepts shared scope", id: "gitea", data: wf("", "permissions:\n  contents: read\n")},
		{name: "gitea unknown scope", id: "gitea", data: wf("", "permissions:\n  bogus: read\n"), wantErr: "bogus"},
		{name: "gitea bad scope value", id: "gitea", data: wf("", "permissions:\n  code: banana\n"), wantErr: "banana"},
		{name: "gitea scope level none", id: "gitea", data: wf("", "permissions:\n  code: none\n")},
		{name: "forgejo non-bool openid-connect", id: "forgejo", data: wf("enable-openid-connect: \"x\"\n", ""), wantErr: "enable-openid-connect"},
		{name: "forgejo non-bool email notifications", id: "forgejo", data: wf("enable-email-notifications: 3\n", ""), wantErr: "enable-email-notifications"},
		{name: "forgejo non-bool job openid-connect", id: "forgejo", data: []byte("on: push\njobs:\n  a:\n    runs-on: r\n    enable-openid-connect: \"x\"\n    steps:\n      - run: echo\n"), wantErr: "enable-openid-connect"},
		{name: "gitea plain file", id: "gitea", data: wf("", ""), wantErr: ""},
	}
	for _, c := range cases {
		err := validate.File(c.id, "ci.yml", c.data)
		switch {
		case c.wantErr == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
			t.Errorf("%s: want error containing %q, got %v", c.name, c.wantErr, err)
		}
	}
}

func TestDialectPatterns(t *testing.T) {
	for id, want := range map[string]string{"gitea": ".gitea/workflows/*.yml", "forgejo": ".forgejo/workflows/*.yaml"} {
		got := strings.Join(validate.Patterns(id), " ")
		if !strings.Contains(got, want) {
			t.Errorf("%s patterns %q lack %s", id, got, want)
		}
	}
}
