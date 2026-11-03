package build_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMathPackage checks every math type against values computed elsewhere,
// and checks BigInt division by its defining identity over random operands.
func TestMathPackage(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");
import std("math/*");

namespace Main {
    static fun flt(flt) r = (x) {
        return type(Math).Round(x * 1000000.0) / 1000000.0 + 0.0;
    }

    static fun BigInt*(str) big = (s) {
        return type(BigInt).Parse(s).first;
    }

    static fun str() basics = () {
        var str s = type(Math).Sqrt(2.0){str} + " " + type(Math).Pow(2.0, 10.0){str} + " " + type(Math).Floor(-1.5){str};
        s = s + " " + type(Math).Mod(7.5, 2.0){str} + " " + type(Math).Round(2.5){str} + " " + type(Math).Log(type(Constants).E){str};
        s = s + " " + type(Flts).NegInf{str} + " " + type(Math).IsNaN(type(Flts).NaN){str} + " " + type(Math).CopySign(3.0, -0.0){str};
        s = s + " " + type(Math).Gcd(84U, 36U){str} + " " + type(Math).Lcm(4U, 6U){str} + " " + type(Math).PowInt(-3, 5U){str};
        s = s + " " + type(Math).Hypot(3.0, 4.0){str} + " " + type(Math).Fma(2.0, 3.0, 1.0){str} + " " + type(Math).Clamp(5.0, 0.0, 1.0){str};
        s = s + " " + type(Trig).Sin(type(Constants).Pi / 2.0){str} + " " + (type(Trig).Atan2(1.0, 1.0) * 4.0){str} + " " + type(Trig).ToDegrees(type(Constants).Pi){str};
        return s;
    }

    static fun str() bits = () {
        var str s = type(Uints).Ones(255U){str} + " " + type(Uints).LeadingZeros(1U){str} + " " + type(Uints).TrailingZeros(8U){str} + " " + type(Uints).TrailingZeros(0U){str};
        s = s + " " + (type(Uints).Reverse(1U) == 9223372036854775808U){str} + " " + (type(Uints).ReverseBytes(1U) == 72057594037927936U){str};
        s = s + " " + type(Uints).RotateLeft(9223372036854775809U, 1U){str} + " " + type(Flts).ToBits(1.0){str} + " " + type(Flts).FromBits(4607182418800017408U){str};
        s = s + " " + type(Uints).NextPowerOfTwo(33U){str} + " " + type(Uints).Len(255U){str} + " " + type(Uints).IsPowerOfTwo(64U){str};
        return s;
    }

    static fun str() hashes = () {
        var Pair[byte*, int] a = type(Strs).ToBytes("a");
        var Pair[byte*, int] abc = type(Strs).ToBytes("abc");
        var Pair[byte*, int] fox = type(Strs).ToBytes("the quick brown fox jumps over the lazy dog!");
        var Pair[byte*, int] hello = type(Strs).ToBytes("hello");
        var Pair[byte*, int] g = type(Strs).ToBytes("abcdefg");
        var str s = type(Hash).FNV1a(a.first, 1U){str} + " " + type(Hash).XXHash64(abc.first, 3U, 0U){str};
        s = s + " " + type(Hash).XXHash64(fox.first, fox.second{uint}, 0U){str} + " " + type(Hash).XXHash64(fox.first, fox.second{uint}, 7U){str};
        s = s + " " + type(Hash).Murmur3(hello.first, 5U, 0U){str} + " " + type(Hash).Murmur3(g.first, 7U, 3U){str};
        s = s + " " + (type(Hash).Str("abc") == type(Hash).XXHash64(abc.first, 3U, 0U)){str} + " " + (type(Hash).Flt(0.0) == type(Hash).Flt(-0.0)){str};
        return s;
    }

    static fun str() values = () {
        var Complex a = type(Complex).Of(1.0, 2.0);
        var Complex b = type(Complex).Of(3.0, -4.0);
        var Complex p = a.Mul(b);
        var Complex q = p.Div(b);
        var Complex root = type(Complex).Of(-4.0, 0.0).Sqrt();
        var str s = p.Str() + " " + q.Str() + " " + root.Str() + " " + b.Abs(){str} + " " + (&p){str};
        var Complex e = type(Complex).Of(0.0, type(Constants).Pi).Exp();
        s = s + " " + type(Main).r(e.re){str} + "," + type(Main).r(e.im){str};

        var Quaternion rot = type(Quaternion).FromAxisAngle(0.0, 0.0, 2.0, type(Constants).Pi / 2.0);
        var Vector* v = rot.Rotate(type(Vector).New(3U, 1.0, 0.0, 0.0));
        s = s + " " + type(Main).r(v.Get(0U)){str} + "," + type(Main).r(v.Get(1U)){str} + "," + type(Main).r(v.Get(2U)){str};
        var Quaternion half = type(Quaternion).Identity().Slerp(rot, 0.5);
        s = s + " " + type(Main).r(half.Mul(half).Sub(rot).Norm()){str} + " " + type(Main).r(rot.Mul(rot.Inverse()).w){str};

        var Fraction f = type(Fraction).Of(1, 2).Add(type(Fraction).Of(1, 3));
        s = s + " " + f.Str() + " " + type(Fraction).Of(3, 4).Mul(type(Fraction).Of(2, 3)).Str() + " " + type(Fraction).Of(4, -8).Str();
        s = s + " " + type(Fraction).Of(1, 3).Cmp(type(Fraction).Of(1, 2)){str} + " " + type(Fraction).Of(6, 3).Str() + " " + f.Div(f).Str();
        return s;
    }

    static fun str() linear = () {
        var Vector* a = type(Vector).New(3U, 1.0, 2.0, 3.0);
        var Vector* b = type(Vector).New(3U, 4.0, 5.0, 6.0);
        var str s = a.Dot(b){str} + " " + a.Cross(b){str} + " " + type(Vector).New(2U, 3.0, 4.0).Norm(){str} + " " + a.Add(b){str};
        s = s + " " + (a == a.Copy()){str} + " " + (a == b){str} + " " + (a.Hash() == a.Copy().Hash()){str} + " " + a.Len(){str};

        var Matrix* m = type(Matrix).New(2U, 2U, 1.0, 2.0, 3.0, 4.0);
        var Pair[Matrix*, bool] inv = m.Inverse();
        s = s + " " + m.Determinant(){str} + " " + inv.first{str} + " " + m.Mul(type(Matrix).Identity(2U)){str} + " " + m.Transpose(){str};
        s = s + " " + m.MulVector(type(Vector).New(2U, 1.0, 1.0)){str} + " " + type(Matrix).New(2U, 2U, 1.0, 2.0, 2.0, 4.0).Inverse().second{str};
        s = s + " " + type(Matrix).Identity(3U).Determinant(){str} + " " + (m == m.Copy()){str};
        return s;
    }

    static fun str() bigs = () {
        var BigInt* f = type(BigInt).FromUint(1U);
        for(var uint i = 2U; i <= 30U; i++) {
            f = f.Mul(type(BigInt).FromUint(i));
        }
        var str s = f.Str() + " " + type(BigInt).FromInt(2).Pow(100U).Str() + " " + type(BigInt).FromInt(-10).Pow(30U).Str();
        var BigInt* a = type(BigInt).FromInt(2).Pow(200U).Add(type(BigInt).FromInt(12345));
        var Pair[BigInt*, BigInt*] d = a.DivMod(type(Main).big("100000000000000000007"));
        s = s + " " + d.first.Str() + " " + d.second.Str();
        d = type(Main).big("170141183420855150474555134919112130560").DivMod(type(Main).big("39614081257132168796771975169"));
        s = s + " " + d.first.Str() + " " + d.second.Str();
        d = type(BigInt).FromInt(-7).DivMod(type(BigInt).FromInt(2));
        s = s + " " + d.first.Str() + " " + d.second.Str();
        s = s + " " + type(BigInt).FromInt(2).Pow(100U).Shr(37U).Str() + " " + type(BigInt).FromInt(3).Pow(50U).Shl(45U).Str();
        s = s + " " + type(BigInt).FromInt(462).Gcd(type(BigInt).FromInt(-1071)).Str() + " " + type(BigInt).FromInt(-9223372036854775807).Sub(type(BigInt).FromInt(1)).ToInt().first{str};
        s = s + " " + type(BigInt).FromInt(2).Pow(64U).ToInt().second{str} + " " + type(BigInt).Parse("12x").second{str} + " " + type(BigInt).Parse("-0").first.Str();
        s = s + " " + type(BigInt).FromInt(5).Cmp(type(BigInt).FromInt(-5)){str} + " " + (type(BigInt).FromInt(5) == type(Main).big("5")){str} + " " + type(BigInt).FromInt(2).Pow(70U).ToFlt(){str};

        var BigFraction* h = type(BigFraction).FromInts(1, 3).Add(type(BigFraction).FromInts(1, 6));
        s = s + " " + h.Str() + " " + h.Sub(h).Str() + " " + type(BigFraction).FromInts(-4, -6).Div(type(BigFraction).FromInts(1, 3)).Str() + " " + h.Cmp(type(BigFraction).FromInts(2, 3)){str};
        return s;
    }

    // divisions checks q * b + r == a with |r| < |b| and r taking a's sign
    // over random operands of up to six limbs.
    static fun bool() divisions = () {
        var uint x = 99U;
        var bool ok = true;
        for(var int i = 0; i < 300; i++) {
            var BigInt* a = type(BigInt).FromUint(0U);
            var BigInt* b = type(BigInt).FromUint(0U);
            x = x * 6364136223846793005U + 1442695040888963407U;
            var uint an = (x >> 33U) % 6U + 1U;
            var uint bn = (x >> 40U) % 4U + 1U;
            for(var uint k = 0U; k < an; k++) {
                x = x * 6364136223846793005U + 1442695040888963407U;
                a = a.Shl(32U).Add(type(BigInt).FromUint(x >> 32U));
            }
            for(var uint k = 0U; k < bn; k++) {
                x = x * 6364136223846793005U + 1442695040888963407U;
                var uint limb = x >> 32U;
                if(k == 0U && (x & 1U) == 1U) {
                    limb = limb | 2147483648U;
                }
                b = b.Shl(32U).Add(type(BigInt).FromUint(limb));
            }
            if(b.IsZero()) {
                b = type(BigInt).FromUint(3U);
            }
            if((x & 2U) == 2U) {
                a = a.Neg();
            }
            if((x & 4U) == 4U) {
                b = b.Neg();
            }
            var Pair[BigInt*, BigInt*] d = a.DivMod(b);
            ok = ok && d.first.Mul(b).Add(d.second) == a;
            ok = ok && d.second.Abs().Cmp(b.Abs()) < 0;
            ok = ok && (d.second.IsZero() || d.second.Sign() == a.Sign());
            ok = ok && type(Main).big(a.Str()) == a;
        }
        return ok;
    }

    fun void(uint, str...) main = (argC, argV) {
        var str out = type(Main).basics() + "\n" + type(Main).bits() + "\n" + type(Main).hashes() + "\n";
        out = out + type(Main).values() + "\n" + type(Main).linear() + "\n" + type(Main).bigs() + "\n";
        out = out + type(Main).divisions(){str} + "\n";
        type(File).StdOut.WriteStr(out);
    }
}
`)
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)
	assert.Equal(t, "1.41421 1024 -2 1.5 3 1 -inf true -3 12 12 -243 5 7 1 1 3.14159 180\n"+
		"8 63 3 64 true true 3 4607182418800017408 1 64 8 true\n"+
		"12638187200555641996 4952883123889572249 16753176286265644536 17358589985730111064 613153351 2222828458 true true\n"+
		"11+2i 1+2i 0+2i 5 11+2i -1,0 0,1,0 0 1 5/6 1/2 -1/2 -1 2 1\n"+
		"32 [-3, 6, -3] 5 [5, 7, 9] true false true 3 -2 [[-2, 1], [1.5, -0.5]] [[1, 2], [3, 4]] [[1, 3], [2, 4]] [3, 7] false 1 true\n"+
		"265252859812191058636308480000000 1267650600228229401496703205376 1000000000000000000000000000000 "+
		"16069380442589902754294764292430332832421 39643735780505486774 4294967294 39614081257132168792477007874 -3 -1 "+
		"9223372036854775808 25258789920773876895098112514266759168 21 -9223372036854775808 false false 0 1 true 1.18059e+21 "+
		"1/2 0 2 -1\n"+
		"true\n", stdout)
}
