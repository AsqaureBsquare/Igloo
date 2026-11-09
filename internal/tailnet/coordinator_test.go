package tailnet

import "testing"

func TestNextIPIsUniqueAndUsable(t *testing.T) {
	c := NewCoordinator()
	seen := map[string]bool{}
	for i := 0; i < 70000; i++ {
		ip := c.nextIP()
		b := ip.As4()
		if b[0] != 100 || b[1] != 64 {
			t.Fatalf("outside 100.64.0.0/16: %s", ip)
		}
		if b[3] == 0 || b[3] == 1 || b[3] == 255 {
			t.Fatalf("reserved host address handed out: %s", ip)
		}
		if i < 253*256 { // the /16 holds 253 usable hosts x 256 subnets, then wraps
			if seen[ip.String()] {
				t.Fatalf("duplicate address %s after %d allocations", ip, i)
			}
			seen[ip.String()] = true
		}
	}
}
