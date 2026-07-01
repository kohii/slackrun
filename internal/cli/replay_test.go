package cli

import "testing"

func TestParsePermalink_TopLevel(t *testing.T) {
	t.Parallel()
	ch, ts, thread, err := parsePermalink("https://henry-app.slack.com/archives/C01DRUDMA2C/p1782906914616759")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if ch != "C01DRUDMA2C" || ts != "1782906914.616759" || thread != "" {
		t.Errorf("got ch=%q ts=%q thread=%q", ch, ts, thread)
	}
}

func TestParsePermalink_ThreadReply(t *testing.T) {
	t.Parallel()
	ch, ts, thread, err := parsePermalink("https://henry-app.slack.com/archives/C01DRUDMA2C/p1782906920000000?thread_ts=1782906914.616759&cid=C01DRUDMA2C")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if ch != "C01DRUDMA2C" || ts != "1782906920.000000" || thread != "1782906914.616759" {
		t.Errorf("got ch=%q ts=%q thread=%q", ch, ts, thread)
	}
}

func TestParsePermalink_Rejects(t *testing.T) {
	t.Parallel()
	cases := []string{
		"",
		"https://slack.com/foo",
		"https://henry-app.slack.com/archives/C01/pnotdigits",
	}
	for _, c := range cases {
		if _, _, _, err := parsePermalink(c); err == nil {
			t.Errorf("expected error for %q", c)
		}
	}
}
