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
        return Math::Round(x * 1000000.0) / 1000000.0 + 0.0;
    }

    static fun BigInt*(str) big = (s) {
        return BigInt::Parse(s).0;
    }

    static fun str() basics = () {
        var str s = Math::Sqrt(2.0){str} + " " + Math::Pow(2.0, 10.0){str} + " " + Math::Floor(-1.5){str};
        s = s + " " + Math::Mod(7.5, 2.0){str} + " " + Math::Round(2.5){str} + " " + Math::Log(Constants::E){str};
        s = s + " " + Flts::NegInf{str} + " " + Math::IsNaN(Flts::NaN){str} + " " + Math::CopySign(3.0, -0.0){str};
        s = s + " " + Math::Gcd(84U, 36U){str} + " " + Math::Lcm(4U, 6U){str} + " " + Math::PowInt(-3, 5U){str};
        s = s + " " + Math::Hypot(3.0, 4.0){str} + " " + Math::Fma(2.0, 3.0, 1.0){str} + " " + Math::Clamp(5.0, 0.0, 1.0){str};
        s = s + " " + Trig::Sin(Constants::Pi / 2.0){str} + " " + (Trig::Atan2(1.0, 1.0) * 4.0){str} + " " + Trig::ToDegrees(Constants::Pi){str};
        return s;
    }

    static fun str() bits = () {
        var str s = Uints::Ones(255U){str} + " " + Uints::LeadingZeros(1U){str} + " " + Uints::TrailingZeros(8U){str} + " " + Uints::TrailingZeros(0U){str};
        s = s + " " + (Uints::Reverse(1U) == 9223372036854775808U){str} + " " + (Uints::ReverseBytes(1U) == 72057594037927936U){str};
        s = s + " " + Uints::RotateLeft(9223372036854775809U, 1U){str} + " " + Flts::ToBits(1.0){str} + " " + Flts::FromBits(4607182418800017408U){str};
        s = s + " " + Uints::NextPowerOfTwo(33U){str} + " " + Uints::Len(255U){str} + " " + Uints::IsPowerOfTwo(64U){str};
        return s;
    }

    static fun str() hashes = () {
        var (byte*, int) a = Strs::ToBytes("a");
        var (byte*, int) abc = Strs::ToBytes("abc");
        var (byte*, int) fox = Strs::ToBytes("the quick brown fox jumps over the lazy dog!");
        var (byte*, int) hello = Strs::ToBytes("hello");
        var (byte*, int) g = Strs::ToBytes("abcdefg");
        var str s = Hash::FNV1a(a.0, 1U){str} + " " + Hash::XXHash64(abc.0, 3U, 0U){str};
        s = s + " " + Hash::XXHash64(fox.0, fox.1{uint}, 0U){str} + " " + Hash::XXHash64(fox.0, fox.1{uint}, 7U){str};
        s = s + " " + Hash::Murmur3(hello.0, 5U, 0U){str} + " " + Hash::Murmur3(g.0, 7U, 3U){str};
        s = s + " " + (Hash::Str("abc") == Hash::XXHash64(abc.0, 3U, 0U)){str} + " " + (Hash::Flt(0.0) == Hash::Flt(-0.0)){str};
        return s;
    }

    static fun str() values = () {
        var Complex a = Complex::Of(1.0, 2.0);
        var Complex b = Complex::Of(3.0, -4.0);
        var Complex p = a.Mul(b);
        var Complex q = p.Div(b);
        var Complex root = Complex::Of(-4.0, 0.0).Sqrt();
        var str s = p.Str() + " " + q.Str() + " " + root.Str() + " " + b.Abs(){str} + " " + (&p){str};
        var Complex e = Complex::Of(0.0, Constants::Pi).Exp();
        s = s + " " + Main::r(e.re){str} + "," + Main::r(e.im){str};

        var Quaternion rot = Quaternion::FromAxisAngle(0.0, 0.0, 2.0, Constants::Pi / 2.0);
        var Vector* v = rot.Rotate(Vector::New(3U, 1.0, 0.0, 0.0));
        s = s + " " + Main::r(v.Get(0U)){str} + "," + Main::r(v.Get(1U)){str} + "," + Main::r(v.Get(2U)){str};
        var Quaternion half = Quaternion::Identity().Slerp(rot, 0.5);
        s = s + " " + Main::r(half.Mul(half).Sub(rot).Norm()){str} + " " + Main::r(rot.Mul(rot.Inverse()).w){str};

        var Fraction f = Fraction::Of(1, 2).Add(Fraction::Of(1, 3));
        s = s + " " + f.Str() + " " + Fraction::Of(3, 4).Mul(Fraction::Of(2, 3)).Str() + " " + Fraction::Of(4, -8).Str();
        s = s + " " + Fraction::Of(1, 3).Cmp(Fraction::Of(1, 2)){str} + " " + Fraction::Of(6, 3).Str() + " " + f.Div(f).Str();
        return s;
    }

    static fun str() linear = () {
        var Vector* a = Vector::New(3U, 1.0, 2.0, 3.0);
        var Vector* b = Vector::New(3U, 4.0, 5.0, 6.0);
        var str s = a.Dot(b){str} + " " + a.Cross(b){str} + " " + Vector::New(2U, 3.0, 4.0).Norm(){str} + " " + a.Add(b){str};
        s = s + " " + (a == a.Copy()){str} + " " + (a == b){str} + " " + (a.Hash() == a.Copy().Hash()){str} + " " + a.Len(){str};

        var Matrix* m = Matrix::New(2U, 2U, 1.0, 2.0, 3.0, 4.0);
        var (Matrix*, bool) inv = m.Inverse();
        s = s + " " + m.Determinant(){str} + " " + inv.0{str} + " " + m.Mul(Matrix::Identity(2U)){str} + " " + m.Transpose(){str};
        s = s + " " + m.MulVector(Vector::New(2U, 1.0, 1.0)){str} + " " + Matrix::New(2U, 2U, 1.0, 2.0, 2.0, 4.0).Inverse().1{str};
        s = s + " " + Matrix::Identity(3U).Determinant(){str} + " " + (m == m.Copy()){str};
        return s;
    }

    static fun str() bigs = () {
        var BigInt* f = BigInt::FromUint(1U);
        for(var uint i = 2U; i <= 30U; i++) {
            f = f.Mul(BigInt::FromUint(i));
        }
        var str s = f.Str() + " " + BigInt::FromInt(2).Pow(100U).Str() + " " + BigInt::FromInt(-10).Pow(30U).Str();
        var BigInt* a = BigInt::FromInt(2).Pow(200U).Add(BigInt::FromInt(12345));
        var (BigInt*, BigInt*) d = a.DivMod(Main::big("100000000000000000007"));
        s = s + " " + d.0.Str() + " " + d.1.Str();
        d = Main::big("170141183420855150474555134919112130560").DivMod(Main::big("39614081257132168796771975169"));
        s = s + " " + d.0.Str() + " " + d.1.Str();
        d = BigInt::FromInt(-7).DivMod(BigInt::FromInt(2));
        s = s + " " + d.0.Str() + " " + d.1.Str();
        s = s + " " + BigInt::FromInt(2).Pow(100U).Shr(37U).Str() + " " + BigInt::FromInt(3).Pow(50U).Shl(45U).Str();
        s = s + " " + BigInt::FromInt(462).Gcd(BigInt::FromInt(-1071)).Str() + " " + BigInt::FromInt(-9223372036854775807).Sub(BigInt::FromInt(1)).ToInt().0{str};
        s = s + " " + BigInt::FromInt(2).Pow(64U).ToInt().1{str} + " " + BigInt::Parse("12x").1{str} + " " + BigInt::Parse("-0").0.Str();
        s = s + " " + BigInt::FromInt(5).Cmp(BigInt::FromInt(-5)){str} + " " + (BigInt::FromInt(5) == Main::big("5")){str} + " " + BigInt::FromInt(2).Pow(70U).ToFlt(){str};

        var BigFraction* h = BigFraction::FromInts(1, 3).Add(BigFraction::FromInts(1, 6));
        s = s + " " + h.Str() + " " + h.Sub(h).Str() + " " + BigFraction::FromInts(-4, -6).Div(BigFraction::FromInts(1, 3)).Str() + " " + h.Cmp(BigFraction::FromInts(2, 3)){str};
        return s;
    }

    // divisions checks q * b + r == a with |r| < |b| and r taking a's sign
    // over random operands of up to six limbs.
    static fun bool() divisions = () {
        var uint x = 99U;
        var bool ok = true;
        for(var int i = 0; i < 300; i++) {
            var BigInt* a = BigInt::FromUint(0U);
            var BigInt* b = BigInt::FromUint(0U);
            x = x * 6364136223846793005U + 1442695040888963407U;
            var uint an = (x >> 33U) % 6U + 1U;
            var uint bn = (x >> 40U) % 4U + 1U;
            for(var uint k = 0U; k < an; k++) {
                x = x * 6364136223846793005U + 1442695040888963407U;
                a = a.Shl(32U).Add(BigInt::FromUint(x >> 32U));
            }
            for(var uint k = 0U; k < bn; k++) {
                x = x * 6364136223846793005U + 1442695040888963407U;
                var uint limb = x >> 32U;
                if(k == 0U && (x & 1U) == 1U) {
                    limb = limb | 2147483648U;
                }
                b = b.Shl(32U).Add(BigInt::FromUint(limb));
            }
            if(b.IsZero()) {
                b = BigInt::FromUint(3U);
            }
            if((x & 2U) == 2U) {
                a = a.Neg();
            }
            if((x & 4U) == 4U) {
                b = b.Neg();
            }
            var (BigInt*, BigInt*) d = a.DivMod(b);
            ok = ok && d.0.Mul(b).Add(d.1) == a;
            ok = ok && d.1.Abs().Cmp(b.Abs()) < 0;
            ok = ok && (d.1.IsZero() || d.1.Sign() == a.Sign());
            ok = ok && Main::big(a.Str()) == a;
        }
        return ok;
    }

    fun void(uint, str...) main = (argC, argV) {
        var str out = Main::basics() + "\n" + Main::bits() + "\n" + Main::hashes() + "\n";
        out = out + Main::values() + "\n" + Main::linear() + "\n" + Main::bigs() + "\n";
        out = out + Main::divisions(){str} + "\n";
        File::StdOut.WriteStr(out);
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
