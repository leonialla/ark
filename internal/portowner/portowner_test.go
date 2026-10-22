package portowner

import (
	"net"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/leonialla/ark/internal/config"
)

func TestParseLsof(t *testing.T) {
	out := "p34495\ncssh\nu501\nf3\nf4\np812\ncnode\nu0\nf7\n"
	got := parseLsof(out)
	want := []Owner{{PID: 34495, Command: "ssh", UID: 501}, {PID: 812, Command: "node", UID: 0}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
	if got := parseLsof(""); len(got) != 0 {
		t.Fatalf("empty: %+v", got)
	}
}

func TestSameForward(t *testing.T) {
	db := config.Tunnel{LocalPort: 4333, SSH: "ubuntu@203.0.113.180", Remote: "localhost:4333"}
	alias := config.Tunnel{LocalPort: 6379, SSH: "prod-redis", Remote: "10.0.1.5:6379"}
	withPort := config.Tunnel{LocalPort: 5432, SSH: "deploy@db.example.com:2222", Remote: "localhost:5432"}
	v6 := config.Tunnel{LocalPort: 8080, SSH: "h", Remote: "[::1]:80"}
	cases := []struct {
		name string
		t    config.Tunnel
		cfg  string
		args string
		want bool
	}{
		{"user's original command", db, "", "ssh -fN -L 4333:localhost:4333 ubuntu@203.0.113.180", true},
		{"clustered flags", db, "", "ssh -fNL 4333:localhost:4333 ubuntu@203.0.113.180", true},
		{"attached -L value", db, "", "ssh -N -L4333:localhost:4333 ubuntu@203.0.113.180", true},
		{"loopback bind address", db, "", "ssh -N -L 127.0.0.1:4333:localhost:4333 ubuntu@203.0.113.180", true},
		{"localhost bind address", db, "", "ssh -N -L localhost:4333:localhost:4333 ubuntu@203.0.113.180", true},
		{"options after destination", db, "", "ssh ubuntu@203.0.113.180 -N -L 4333:localhost:4333", true},
		{"-l user", db, "", "ssh -N -l ubuntu -L 4333:localhost:4333 203.0.113.180", true},
		{"one of several -L", db, "", "ssh -N -L 9000:localhost:9000 -L 4333:localhost:4333 ubuntu@203.0.113.180", true},
		{"alias", alias, "", "/usr/bin/ssh -fN -L 6379:10.0.1.5:6379 prod-redis", true},
		{"-p port", withPort, "", "ssh -N -p 2222 -L 5432:localhost:5432 deploy@db.example.com", true},
		{"ssh:// destination", withPort, "", "ssh -N -L 5432:localhost:5432 ssh://deploy@db.example.com:2222", true},
		{"ipv6 remote", v6, "", "ssh -N -L 8080:[::1]:80 h", true},
		{"same -F as config", db, "/x/cfg", "ssh -F /x/cfg -N -L 4333:localhost:4333 ubuntu@203.0.113.180", true},
		{"plain -o is fine", db, "", "ssh -o ServerAliveInterval=30 -N -L 4333:localhost:4333 ubuntu@203.0.113.180", true},

		{"different remote port", db, "", "ssh -N -L 4333:localhost:3306 ubuntu@203.0.113.180", false},
		{"localhost vs 127.0.0.1", db, "", "ssh -N -L 4333:127.0.0.1:4333 ubuntu@203.0.113.180", false},
		{"different host", db, "", "ssh -N -L 4333:localhost:4333 ubuntu@10.0.0.1", false},
		{"different user", db, "", "ssh -N -L 4333:localhost:4333 root@203.0.113.180", false},
		{"missing user", db, "", "ssh -N -L 4333:localhost:4333 203.0.113.180", false},
		{"different ssh port", withPort, "", "ssh -N -L 5432:localhost:5432 deploy@db.example.com", false},
		{"wildcard bind", db, "", "ssh -N -L 0.0.0.0:4333:localhost:4333 ubuntu@203.0.113.180", false},
		{"star bind", db, "", "ssh -N -L *:4333:localhost:4333 ubuntu@203.0.113.180", false},
		{"only other port forwarded", db, "", "ssh -N -L 9000:localhost:4333 ubuntu@203.0.113.180", false},
		{"no -L at all", db, "", "ssh ubuntu@203.0.113.180", false},
		{"jump host", db, "", "ssh -J bastion -N -L 4333:localhost:4333 ubuntu@203.0.113.180", false},
		{"-o HostName", db, "", "ssh -o HostName=198.51.100.1 -N -L 4333:localhost:4333 ubuntu@203.0.113.180", false},
		{"-oProxyCommand attached", db, "", "ssh -oProxyCommand=nc -N -L 4333:localhost:4333 ubuntu@203.0.113.180", false},
		{"different -F", db, "/x/cfg", "ssh -F /other -N -L 4333:localhost:4333 ubuntu@203.0.113.180", false},
		{"-F when config has none", db, "", "ssh -F /x/cfg -N -L 4333:localhost:4333 ubuntu@203.0.113.180", false},
		{"not ssh", db, "", "socat -L 4333:localhost:4333 ubuntu@203.0.113.180", false},
		{"remote forward only", db, "", "ssh -N -R 4333:localhost:4333 ubuntu@203.0.113.180", false},
	}
	for _, c := range cases {
		if got := SameForward(strings.Fields(c.args), c.t, c.cfg); got != c.want {
			t.Errorf("%s: SameForward(%q) = %v, want %v", c.name, c.args, got, c.want)
		}
	}
}

func TestFindAndProcArgs(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	owners, err := Find(l.Addr().(*net.TCPAddr).Port)
	if err != nil {
		t.Fatal(err)
	}
	if len(owners) != 1 || owners[0].PID != os.Getpid() || owners[0].UID != os.Getuid() {
		t.Fatalf("owners = %+v", owners)
	}
	if !reflect.DeepEqual(owners[0].Args, os.Args) {
		t.Fatalf("args = %q, want %q", owners[0].Args, os.Args)
	}
}
