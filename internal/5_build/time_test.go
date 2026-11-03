package build_test

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTime reads instants on the calendar, writes and parses RFC 3339 and
// writes durations, checking each against Go's time package, and runs
// timers, tickers and sleeps against the clock.
func TestTime(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("lang/time/*");
import std("io/local/*");
import std("codec/format/*");

namespace Main {
    static fun void(str, str) put = (name, value) {
        type(File).StdOut.WriteStr(name + " " + value + "\n");
    }

    static fun bool(Timer*) stopLater = (t) {
        type(Timer).Sleep(type(Duration).Milliseconds(20));
        return t.Stop();
    }

    fun void(uint, str...) main = (argC, argV) {
        type(Main).put("now", type(Time).Now().UnixNano(){str});

        var JSON* instants = type(JSON).Parse(argV[0]).first;
        for(var uint i = 0U; i < instants.Len(); i++) {
            var Time t = type(Time).FromUnixNano(instants.At(i).AsInt());
            type(Main).put("instant", t.UnixNano(){str} + " " + t.Year(){str} + " " + t.Month(){str} + " " + t.Day(){str} + " " +
                t.Hour(){str} + " " + t.Minute(){str} + " " + t.Second(){str} + " " + t.Nanosecond(){str} + " " +
                t.Weekday(){str} + " " + t.YearDay(){str} + " " + t.Unix(){str} + " " + t.UnixMilli(){str} + " " + t.Str());
        }

        var JSON* texts = type(JSON).Parse(argV[1]).first;
        for(var uint i = 0U; i < texts.Len(); i++) {
            var Pair[Time, Err*] p = type(Time).Parse(texts.At(i).AsStr());
            if(p.second != null) {
                type(Main).put("parse", "error");
            } else {
                type(Main).put("parse", p.first.UnixNano(){str});
            }
        }

        var JSON* spans = type(JSON).Parse(argV[2]).first;
        for(var uint i = 0U; i < spans.Len(); i++) {
            type(Main).put("duration", type(Duration).Nanoseconds(spans.At(i).AsInt()).Str());
        }

        var Time leap = type(Time).Date(2024, 3, 0, 0, 0, 0, 0);
        var Time carried = type(Time).Date(2023, 14, 1, 25, 0, 0, 0);
        var Time early = type(Time).FromUnix(100, 0);
        var Duration hour = type(Duration).Hours(1);
        type(Main).put("date", leap.Str() + " " + carried.Str() + " " + early.Add(hour).Sub(early).Str() + " " +
            early.Before(early.Add(hour)){str} + " " + early.Add(hour).After(early){str} + " " + early.Equal(type(Time).FromUnixMilli(100000)){str} + " " +
            hour.Add(type(Duration).Minutes(30)).InMinutes(){str} + " " + type(Duration).Seconds(-3).Abs().Cmp(type(Duration).Milliseconds(3000)){str} + " " +
            type(Duration).Seconds(10).Div(4).Mul(2).InMilliseconds(){str});

        var Time start = type(Time).Now();
        type(Timer).Sleep(type(Duration).Milliseconds(50));
        var Duration slept = type(Time).Since(start);

        start = type(Time).Now();
        var Timer* fires = type(Timer).New(type(Duration).Milliseconds(30));
        var bool fired = fires.Wait();
        var Duration waited = type(Time).Since(start);
        var bool stoppedLate = fires.Stop();
        var bool rearmed = fires.Reset(type(Duration).Milliseconds(10));
        var bool firedAgain = fires.Wait();

        start = type(Time).Now();
        var Timer* stopped = type(Timer).New(type(Duration).Seconds(10));
        var thread[bool] stopper = async(type(Main).stopLater(stopped));
        var bool firedEarly = stopped.Wait();
        var bool stoppedInTime = wait(stopper);
        var Duration cut = type(Time).Since(start);
        type(Main).put("timer", (slept.InMilliseconds() >= 50){str} + " " + fired{str} + " " + (waited.InMilliseconds() >= 30){str} + " " +
            stoppedLate{str} + " " + rearmed{str} + " " + firedAgain{str} + " " + firedEarly{str} + " " + stoppedInTime{str} + " " +
            (cut.InSeconds() < 5.0){str} + " " + (type(Time).Until(type(Time).Now().Add(hour)).InMinutes() > 59.0){str});

        start = type(Time).Now();
        var Ticker* ticker = type(Ticker).New(type(Duration).Milliseconds(20));
        var bool ticked = ticker.Wait() && ticker.Wait() && ticker.Wait();
        var Duration ticking = type(Time).Since(start);
        var bool stoppedTicking = ticker.Stop();
        var bool tickedAfterStop = ticker.Wait();
        var bool restarted = ticker.Reset(type(Duration).Milliseconds(10));
        var bool tickedAgain = ticker.Wait();
        type(Main).put("ticker", ticked{str} + " " + (ticking.InMilliseconds() >= 60){str} + " " + (ticking.InSeconds() < 2.0){str} + " " +
            stoppedTicking{str} + " " + tickedAfterStop{str} + " " + restarted{str} + " " + tickedAgain{str} + " " + ticker.Stop(){str});
    }
}
`)
	instants := []int64{
		0, -1, 1, 951782400000000000, 951868800123456789, 1709164800000000000, 1735689599999999999,
		-2208988800000000000, -62135596800 * 1, 4102444800500000000, 9223372036854775807, -9223372036854775807,
		1700000000000000000, 1767225600000000010, -86400000000001,
	}
	texts := []string{
		"2006-01-02T15:04:05Z", "2006-01-02T15:04:05.5+07:00", "1999-12-31t23:59:59.123456789-08:30",
		"2024-02-29T00:00:00z", "1970-01-01 00:00:00Z", "2023-02-29T00:00:00Z", "2006-01-02T24:00:00Z",
		"2006-01-02T15:04:05", "2006-1-02T15:04:05Z", "2006-01-02T15:04:05.Z", "2006-01-02T15:04:05+0700",
	}
	spans := []int64{
		0, 1, 999, 1000, 1500, 999999, 1000000, 1234567, 999999999, 1000000000, 1500000000, 60000000000,
		61500000000, 3600000000000, 3723004005006, -1500, -3600000000000, 9223372036854775807, -9223372036854775807,
	}
	arg := func(v any) string {
		b, err := json.Marshal(v)
		require.NoError(t, err)
		return string(b)
	}

	before := time.Now()
	stdout, stderr, status := run(t, binary, arg(instants), arg(texts), arg(spans))
	after := time.Now()
	require.Zero(t, status, stderr)

	got := map[string][]string{}
	for line := range strings.Lines(stdout) {
		name, value, _ := strings.Cut(strings.TrimSuffix(line, "\n"), " ")
		got[name] = append(got[name], value)
	}

	require.Len(t, got["now"], 1)
	now, err := strconv.ParseInt(got["now"][0], 10, 64)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, now, before.Add(-time.Second).UnixNano())
	assert.LessOrEqual(t, now, after.Add(time.Second).UnixNano())

	require.Len(t, got["instant"], len(instants))
	for i, ns := range instants {
		u := time.Unix(0, ns).UTC()
		want := fmt.Sprintf("%d %d %d %d %d %d %d %d %d %d %d %d %s", ns, u.Year(), u.Month(), u.Day(), u.Hour(), u.Minute(), u.Second(),
			u.Nanosecond(), u.Weekday(), u.YearDay(), u.Unix(), u.UnixMilli(), u.Format(time.RFC3339Nano))
		assert.Equal(t, want, got["instant"][i], "instant %d", ns)
	}

	require.Len(t, got["parse"], len(texts))
	for i, text := range texts {
		want := "error"
		if parsed, err := time.Parse(time.RFC3339Nano, strings.Replace(strings.ToUpper(text), " ", "T", 1)); err == nil && !strings.HasSuffix(text, ".Z") {
			want = strconv.FormatInt(parsed.UnixNano(), 10)
		}
		assert.Equal(t, want, got["parse"][i], text)
	}

	require.Len(t, got["duration"], len(spans))
	for i, ns := range spans {
		assert.Equal(t, time.Duration(ns).String(), got["duration"][i], "duration %d", ns)
	}

	assert.Equal(t, []string{"2024-02-29T00:00:00Z 2024-02-02T01:00:00Z 1h0m0s true true true 90 0 5000"}, got["date"])
	assert.Equal(t, []string{"true true true false false true false true true true"}, got["timer"])
	assert.Equal(t, []string{"true true true true false false true true"}, got["ticker"])
}
