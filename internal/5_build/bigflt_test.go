package build_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBigFlt checks BigFlt at 200 bits against values computed to 140 decimal
// digits elsewhere, including results whose leading bits cancel.
func TestBigFlt(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");
import std("math/*");

namespace Main {
    static fun BigFlt*(str) p = (s) {
        return BigFlt::Parse(s, 200U).0;
    }

    fun void(uint, str...) main = (argC, argV) {
        var str s = BigFlt::Pi(200U).Text(50U) + "\n";
        s = s + BigFlt::E(200U).Text(50U) + "\n";
        s = s + BigFlt::Ln2(200U).Text(50U) + "\n";
        s = s + BigFlt::FromInt(2, 200U).Sqrt().Text(50U) + "\n";
        s = s + Main::p("1.5").Exp().Text(50U) + "\n";
        s = s + Main::p("10").Log().Text(50U) + "\n";
        s = s + Main::p("0.5").Sin().Text(50U) + "\n";
        s = s + Main::p("0.5").Cos().Text(50U) + "\n";
        s = s + Main::p("0.5").Tan().Text(50U) + "\n";
        s = s + Main::p("100").Sin().Text(50U) + "\n";
        s = s + Main::p("2").Atan().Text(50U) + "\n";
        s = s + Main::p("0.3").Atan().Text(50U) + "\n";
        s = s + Main::p("-1000").Atan().Text(50U) + "\n";
        s = s + Main::p("2").Pow(Main::p("0.5")).Text(50U) + "\n";
        s = s + Main::p("2").Pow(Main::p("10")).Text(50U) + "\n";
        s = s + Main::p("2").Pow(Main::p("-3")).Text(50U) + "\n";
        s = s + Main::p("3").Sqrt().Mul(Main::p("10").Exp()).Text(50U) + "\n";
        s = s + BigFlt::Pi(200U).Sin().Text(10U) + "\n";
        s = s + Main::p("1.000000000000000000000000000001").Log().Text(20U) + "\n";
        var BigFlt* tiny = BigFlt::FromInt(2, 400U).Pow(BigFlt::FromInt(-300, 400U));
        s = s + BigFlt::FromInt(1, 400U).Add(tiny).Sub(BigFlt::FromInt(1, 400U)).Text(5U) + "\n";
        s = s + BigFlt::FromFlt(0.1, 53U).Text(20U) + "\n";
        s = s + BigFlt::FromInt(1, 200U).Div(BigFlt::FromInt(3, 200U)).Text(40U) + "\n";
        s = s + Main::p("-123.456e-7").Text(12U) + "\n";
        s = s + BigFlt::Ln2(200U).Mul(Main::p("-37.5")).Text(50U) + "\n";
        s = s + Main::p("-40").Exp().Text(30U) + "\n";

        var BigFlt* h = Main::p("-2.5");
        s = s + h.Floor(){str} + " " + h.Ceil(){str} + " " + h.Round(){str} + " " + h.Trunc(){str} + " " + Main::p("7.5").Round(){str};
        s = s + " " + Main::p("1e100").Text(5U) + " " + Main::p("123456789").Text(5U) + " " + Main::p("-.5"){str};
        s = s + " " + BigFlt::FromInt(1, 100U).Add(BigFlt::FromInt(2, 100U).Pow(BigFlt::FromInt(-300, 100U))){str};
        s = s + " " + (Main::p("0.5") == BigFlt::FromFlt(0.5, 10U)){str} + " " + BigFlt::FromInt(1, 200U).Div(BigFlt::FromInt(3, 200U)).Cmp(Main::p("0.3333")){str};
        s = s + " " + BigFlt::Parse("1.2.3", 64U).1{str} + " " + BigFlt::Parse("1e", 64U).1{str} + " " + Main::p("12.9").ToBigInt(){str};
        s = s + " " + (Main::p("123.25").ToFlt() == 123.25){str} + " " + (BigFlt::FromFlt(0.1, 53U).ToFlt() == 0.1){str} + " " + (BigFlt::Pi(53U).ToFlt() == Constants::Pi){str};
        s = s + " " + (Main::p("1e-300").ToFlt() == 1e-300){str} + " " + Main::p("3").Sub(Main::p("3")).IsZero(){str} + "\n";
        File::StdOut.WriteStr(s);
    }
}
`)
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)
	assert.Equal(t, strings.Join([]string{
		"3.1415926535897932384626433832795028841971693993751",
		"2.7182818284590452353602874713526624977572470937",
		"0.69314718055994530941723212145817656807550013436026",
		"1.4142135623730950488016887242096980785696718753769",
		"4.4816890703380648226020554601192758190057498683697",
		"2.3025850929940456840179914546843642076011014886288",
		"0.4794255386042030002732879352155713880818033679406",
		"0.87758256189037271611628158260382965199164519710974",
		"0.54630248984379051325517946578028538329755172017979",
		"-0.50636564110975879365655761045978543206503272129066",
		"1.1071487177940905030170654601785370400700476454014",
		"0.29145679447786709199560462143289119350316759901207",
		"-1.5697963271282297525647978820048308980869637651333",
		"1.4142135623730950488016887242096980785696718753769",
		"1024",
		"0.125",
		"38150.957867783225999541678147850835129277380346678",
		"1.141993699e-61",
		"1e-30",
		"4.9091e-91",
		"0.10000000000000000555",
		"0.3333333333333333333333333333333333333333",
		"-0.0000123456",
		"-25.99301927099794910314620455468162130283125503851",
		"4.24835425529158899532923478286e-18",
		"-3 -2 -3 -2 8 1e+100 1.2346e+8 -0.5 1 true 1 false false 12 true true true true true",
		"",
	}, "\n"), stdout)
}
