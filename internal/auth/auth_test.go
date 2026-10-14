package auth

import (
	"os/exec"
	"strings"
	"testing"
)

// Compares against the app's JS implementation (main-app.cjs AA/aMt) executed by node.
func TestChecksumMatchesApp(t *testing.T) {
	js := `function aMt(e){let t=165;for(let n=0;n<e.length;n++)(e[n]=(e[n]^t)+(n%256)),(t=e[n]);return e}
function AA(now,e){let t=Math.floor(now/1e6),n=new Uint8Array([(t>>40)&255,(t>>32)&255,(t>>24)&255,(t>>16)&255,(t>>8)&255,t&255]);return Buffer.from(aMt(n)).toString("base64url")+e}
for (const ms of [1760000000000, 1792000000000, 1700000123456]) console.log(AA(ms,"mid"))`
	out, err := exec.Command("node", "-e", js).Output()
	if err != nil {
		t.Skip("node not available")
	}
	want := strings.Fields(string(out))
	for i, ms := range []int64{1760000000000, 1792000000000, 1700000123456} {
		if got := checksumAt(ms, "mid"); got != want[i] {
			t.Fatalf("ms=%d got %s want %s", ms, got, want[i])
		}
	}
}

func TestStartLoginPKCE(t *testing.T) {
	lr := StartLogin()
	if !strings.Contains(lr.URL, "redirectTarget=sand") || !strings.Contains(lr.URL, "challenge=") || len(lr.Verifier) != 43 {
		t.Fatalf("bad login request %+v", lr)
	}
}
