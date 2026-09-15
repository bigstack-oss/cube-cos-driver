package enterprise

import "testing"

// The advisor installer is the only route a new Advisor build has onto a
// deployed framework, and everything it must not disturb on the way through is
// invisible until a re-run actually happens. These assert on the embedded
// script because that is what ships: the driver pushes this text to the node.

func TestAdvisorInstallDoesNotSkipAnExistingRelease(t *testing.T) {
	if contains(installAdvisorScript, "already installed — skipping helm install") {
		t.Error("the advisor installer skips when the release exists, so a new chart " +
			"version can never reach a deployed framework; helm upgrade --install " +
			"is the command for may-or-may-not-exist and needs no guard")
	}
	if !contains(installAdvisorScript, "helm upgrade --install cube-advisor") {
		t.Error("the advisor installer must use helm upgrade --install: plain helm " +
			"install cannot re-run, which is what made the guard look necessary")
	}
}

// An upgrade renders the whole release. The enrollment Secret is not a value
// this script generates, so passing nothing back renders without it and helm
// deletes it — every enrolled agent pins that CA and cannot be re-enrolled
// remotely. The cost of getting this wrong is not a failed upgrade, it is a
// successful one that silently breaks every cluster already enrolled.
func TestAdvisorInstallCarriesEnrollmentThroughAnUpgrade(t *testing.T) {
	for _, want := range []string{
		"get secret cube-advisor-enrollment",
		"--set-file enrollment.caCert=",
		"--set-file enrollment.caKey=",
	} {
		if !contains(installAdvisorScript, want) {
			t.Errorf("the advisor installer does not read back %q; an upgrade would "+
				"drop the enrollment Secret and break every enrolled agent", want)
		}
	}
}

// The passwords are reused on re-run for a reason the script states: a re-run
// must not rotate creds a prior install wrote into the running database. The
// web keypair is the same shape of promise to a browser.
// Asserting on the guarding condition, not merely that the secret is named
// somewhere: a first draft of this test looked for the name alone and passed
// while the reuse branch was disabled, because the name still appeared inside
// the dead body.
func TestAdvisorInstallReusesTheWebKeypair(t *testing.T) {
	if !contains(installAdvisorScript, `if $K -n "$NS" get secret cube-advisor-web-tls`) {
		t.Error("the advisor installer does not condition on an existing TLS keypair, " +
			"so a routine upgrade rotates it and every browser that has accepted " +
			"this advisor challenges it again")
	}
	if !contains(installAdvisorScript, `if [ ! -s "$TLSDIR/tls.crt" ]`) {
		t.Error("the advisor installer issues a certificate unconditionally; it must " +
			"only do so when it could not reuse the existing one")
	}
}

// The two installers differ on purpose, and the difference is the helm verb.
// Without this the asymmetry reads as an oversight and someone removes the
// portal's guard too — which would break it, because plain helm install
// refuses a name already in use.
func TestPortalKeepsItsGuardBecauseItCannotUpgrade(t *testing.T) {
	if contains(installPortalScript, "helm upgrade --install") {
		t.Skip("the portal installer now upgrades in place; its guard can go too")
	}
	if !contains(installPortalScript, "already installed — skipping helm install") {
		t.Error("the portal installer runs plain helm install but no longer guards " +
			"against an existing release, so a re-run fails on the name in use")
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// An advisor with no enrollment CA installs cleanly and cannot enrol anything:
// the API registers enrollment only when both CA halves are present, so
// /api/v1/enroll and /api/v1/releases answer 404 and the log reads
// "enrollment disabled (need -dsn, -ca-cert and -ca-key)". Installing an
// Advisor that no cluster can reach is not a successful install.
func TestAdvisorInstallIssuesAnEnrollmentCAWhenThereIsNone(t *testing.T) {
	for _, want := range []string{
		// the else branch of the carry-through check — generated only when absent
		"could not generate the enrollment CA key",
		"could not issue the enrollment CA certificate",
		"--set-file enrollment.tunnelCert=",
		"--set-file enrollment.tunnelKey=",
	} {
		if !contains(installAdvisorScript, want) {
			t.Errorf("the advisor installer does not issue %q; an install with no "+
				"pre-existing Secret leaves enrollment disabled and no cluster "+
				"can ever enrol", want)
		}
	}
}

// The agent pins the enrollment CA and offers no flag to loosen it, so the
// tunnel certificate must name the address agents dial. A CN-only certificate,
// or a SAN naming anything else, is one no agent can connect through.
func TestAdvisorTunnelCertificateNamesTheDialledAddress(t *testing.T) {
	if !contains(installAdvisorScript, "subjectAltName=IP:%s") {
		t.Error("the tunnel certificate carries no IP SAN for the advisor LB address; " +
			"agents pin this CA and cannot connect to a certificate that does not name it")
	}
}

// Enrollment being registered is the point of installing an Advisor, and it is
// invisible until some cluster tries months later. An unauthenticated POST must
// be refused (401), never missing (404) — 404 is the shape of a disabled
// endpoint, which is exactly the failure this whole change is about.
func TestAdvisorInstallVerifiesEnrollmentIsLive(t *testing.T) {
	if !contains(installAdvisorScript, "/api/v1/enroll") {
		t.Error("the advisor installer never checks that enrollment is registered, " +
			"so an advisor that cannot enrol anything still reports success")
	}
	if !contains(installAdvisorScript, "enrollment is disabled") {
		t.Error("the advisor installer does not fail on a 404 from /api/v1/enroll")
	}
}

// CMP and the app framework's Keycloak sit behind one ingress, so they share an
// upstream — and advisor-api refuses a config where two origins share one. They
// must land on a single origin for a second reason that costs a login rather
// than a config error: the OIDC state cookie would otherwise be set on one
// origin and the callback arrive at the other.
func TestAdvisorConsolePutsCMPAndItsIdPOnOneOrigin(t *testing.T) {
	for _, want := range []string{
		"webConsole.origins[$n].targets[0]=cube-cmp",
		"webConsole.origins[$n].targets[1]=app-fw-idp",
	} {
		if !contains(installAdvisorScript, want) {
			t.Errorf("the installer does not place %q on the ingress origin; CMP and "+
				"its IdP on separate origins means the OIDC callback lands on the "+
				"wrong cookie jar and login fails", want)
		}
	}
}

// Target names are the node's, not the advisor's: cubecos allows cube-cos,
// cube-cmp and app-fw-idp, and a name the node does not allow is one the agent
// refuses to dial.
func TestAdvisorConsoleUsesTheNodesTargetNames(t *testing.T) {
	for _, want := range []string{"cube-cmp", "app-fw-idp", "cube-cos"} {
		if !contains(installAdvisorScript, want) {
			t.Errorf("the installer never names target %q, which is what cubecos allows", want)
		}
	}
	// The chart's example names are not the node's; emitting them would produce
	// a console whose every target is refused.
	for _, bad := range []string{"cmp-portal", "cmp-idp", "targets[0]=dashboard"} {
		if contains(installAdvisorScript, bad) {
			t.Errorf("the installer emits %q, which no cubecos node allows", bad)
		}
	}
}

// Every console origin address must be a SAN on the advisor's certificate:
// advisor-api refuses to enable the console when one is not covered, naming it.
// Getting this wrong produces an install that succeeds and a console that never
// starts.
func TestAdvisorCertificateCoversEveryConsoleAddress(t *testing.T) {
	if !contains(installAdvisorScript, `SANS="$SANS,IP:$a"`) {
		t.Error("the installer does not add console addresses to the certificate SANs; " +
			"advisor-api refuses to enable a console whose address its certificate " +
			"does not cover")
	}
	if !contains(installAdvisorScript, `-addext "subjectAltName=${SANS}"`) {
		t.Error("the accumulated SAN list is not passed to openssl")
	}
}

// An install with no pool must behave exactly as it did before the pool
// existed: console off, not a half-configured one.
func TestAdvisorConsoleStaysOffWithoutAPool(t *testing.T) {
	if !contains(installAdvisorScript, `CONSOLE_POOL="${5:-}"`) {
		t.Error("the pool argument is not optional; every existing caller passes four args")
	}
	if !contains(installAdvisorScript, `if [ "${#POOL_ADDRS[@]}" -ge 2 ]`) {
		t.Error("the installer enables the console without requiring at least two " +
			"addresses; one address cannot carry both the CMP origin and the " +
			"node's own dashboard")
	}
}
