#include "posit.h"

#include <math.h>

__extension__ typedef unsigned __int128 Wide;

#define MAXPOS (ZAMPOSIT_NAR - 1)
#define MINPOS ((ZamPosit)1)
// MAX_SCALE is the scale of MAXPOS: a regime of 62 ones, at 4 a step.
#define MAX_SCALE 248

// Unpacked is a nonzero real posit as sign × 2^scale × significand / 2^63, the
// significand's leading one at bit 63.
typedef struct Unpacked {
    bool negative;
    int32_t scale;
    uint64_t significand;
} Unpacked;

static Unpacked unpack(ZamPosit posit) {
    Unpacked unpacked = {.negative = (int64_t)posit < 0};
    uint64_t bits = unpacked.negative ? -posit : posit;
    uint64_t rest = bits << 1;
    int run;
    int32_t k;
    if (rest >> 63) {
        run = __builtin_clzll(~rest);
        k = run - 1;
    } else {
        run = __builtin_clzll(rest);
        k = -run;
    }
    rest = run + 1 >= 64 ? 0 : rest << (run + 1);
    unpacked.scale = 4 * k + (int32_t)(rest >> 62);
    unpacked.significand = ((uint64_t)1 << 63) | (rest << 2 >> 1);
    return unpacked;
}

// pack rounds sign × 2^scale × significand / 2^63, with sticky saying whether
// anything nonzero lies below the significand, to the nearest posit.
static ZamPosit pack(bool negative, int32_t scale, uint64_t significand,
                     bool sticky) {
    ZamPosit bits;
    if (scale >= MAX_SCALE) {
        bits = MAXPOS;
    } else if (scale < -MAX_SCALE) {
        bits = MINPOS;
    } else {
        int32_t k = scale >= 0 ? scale / 4 : -((-scale + 3) / 4);
        uint64_t exponent = (uint64_t)(scale - 4 * k);
        int regimeLength;
        uint64_t regime;
        if (k >= 0) {
            regimeLength = k + 2;
            regime = (((uint64_t)1 << (k + 1)) - 1) << 1;
        } else {
            regimeLength = -k + 1;
            regime = 1;
        }

        // The pattern after the sign, laid from the top of 128 bits. The
        // fraction's 63 bits end in the significand's always-zero bit 0 when
        // they are shifted one short of fitting.
        Wide pattern = (Wide)regime << (128 - regimeLength);
        pattern |= (Wide)exponent << (128 - regimeLength - 2);
        uint64_t fraction = significand << 1;
        int shift = 128 - regimeLength - 2 - 64;
        pattern |= shift >= 0 ? (Wide)fraction << shift : fraction >> -shift;

        bits = (uint64_t)(pattern >> 65);
        bool guard = (pattern >> 64) & 1;
        bool below = (uint64_t)pattern != 0 || sticky;
        if (guard && (below || (bits & 1)))
            bits++;
        if (bits >= ZAMPOSIT_NAR)
            bits = MAXPOS;
        else if (bits == 0)
            bits = MINPOS;
    }
    return negative ? -bits : bits;
}

static int leadingZeros(Wide value) {
    uint64_t high = (uint64_t)(value >> 64);
    return high != 0 ? __builtin_clzll(high)
                     : 64 + __builtin_clzll((uint64_t)value);
}

// shiftRightJam shifts right, folding every bit shifted out into the lowest.
static Wide shiftRightJam(Wide value, int32_t shift) {
    if (shift == 0)
        return value;
    if (shift >= 128)
        return value != 0;
    return (value >> shift) | ((value << (128 - shift)) != 0);
}

ZamPosit zam_posit_add(ZamPosit left, ZamPosit right) {
    if (left == ZAMPOSIT_NAR || right == ZAMPOSIT_NAR)
        return ZAMPOSIT_NAR;
    if (left == 0)
        return right;
    if (right == 0)
        return left;

    Unpacked larger = unpack(left);
    Unpacked smaller = unpack(right);
    if (larger.scale < smaller.scale ||
        (larger.scale == smaller.scale &&
         larger.significand < smaller.significand)) {
        Unpacked swap = larger;
        larger = smaller;
        smaller = swap;
    }

    // Leading ones at bit 126, leaving a bit for the carry and 63 below for
    // what the alignment shifts out.
    Wide sum = (Wide)larger.significand << 63;
    Wide addend = shiftRightJam((Wide)smaller.significand << 63,
                                larger.scale - smaller.scale);
    sum = larger.negative == smaller.negative ? sum + addend : sum - addend;
    if (sum == 0)
        return 0;

    int zeros = leadingZeros(sum);
    sum <<= zeros;
    return pack(larger.negative, larger.scale + 1 - zeros,
                (uint64_t)(sum >> 64), (uint64_t)sum != 0);
}

ZamPosit zam_posit_sub(ZamPosit left, ZamPosit right) {
    return zam_posit_add(left, -right);
}

ZamPosit zam_posit_mul(ZamPosit left, ZamPosit right) {
    if (left == ZAMPOSIT_NAR || right == ZAMPOSIT_NAR)
        return ZAMPOSIT_NAR;
    if (left == 0 || right == 0)
        return 0;

    Unpacked l = unpack(left);
    Unpacked r = unpack(right);
    Wide product = (Wide)l.significand * r.significand;
    int32_t scale = l.scale + r.scale;
    if (product >> 127)
        scale++;
    else
        product <<= 1;
    return pack(l.negative != r.negative, scale, (uint64_t)(product >> 64),
                (uint64_t)product != 0);
}

ZamPosit zam_posit_div(ZamPosit left, ZamPosit right) {
    if (left == ZAMPOSIT_NAR || right == ZAMPOSIT_NAR || right == 0)
        return ZAMPOSIT_NAR;
    if (left == 0)
        return 0;

    Unpacked l = unpack(left);
    Unpacked r = unpack(right);
    Wide dividend = (Wide)l.significand << 64;
    Wide quotient = dividend / r.significand;
    bool sticky = dividend % r.significand != 0;
    int32_t scale = l.scale - r.scale;
    if (quotient >> 64) {
        sticky |= quotient & 1;
        quotient >>= 1;
    } else {
        scale--;
    }
    return pack(l.negative != r.negative, scale, (uint64_t)quotient, sticky);
}

static ZamPosit fromMagnitude(bool negative, uint64_t magnitude) {
    if (magnitude == 0)
        return 0;
    int zeros = __builtin_clzll(magnitude);
    return pack(negative, 63 - zeros, magnitude << zeros, false);
}

ZamPosit zam_posit_from_int(int64_t value) {
    return fromMagnitude(value < 0,
                         value < 0 ? -(uint64_t)value : (uint64_t)value);
}

ZamPosit zam_posit_from_uint(uint64_t value) {
    return fromMagnitude(false, value);
}

ZamPosit zam_posit_from_fraction(uint64_t value) {
    if (value == 0)
        return 0;
    int zeros = __builtin_clzll(value);
    return pack(false, -1 - zeros, value << zeros, false);
}

ZamPosit zam_posit_from_double(double value) {
    if (isnan(value) || isinf(value))
        return ZAMPOSIT_NAR;
    if (value == 0)
        return 0;

    uint64_t bits;
    memcpy(&bits, &value, sizeof bits);
    bool negative = bits >> 63;
    int32_t exponent = (int32_t)((bits >> 52) & 0x7FF);
    uint64_t mantissa = bits & (((uint64_t)1 << 52) - 1);
    if (exponent == 0) {
        int zeros = __builtin_clzll(mantissa);
        return pack(negative, -1074 + 63 - zeros, mantissa << zeros, false);
    }
    return pack(negative, exponent - 1023,
                (mantissa | ((uint64_t)1 << 52)) << 11, false);
}

int64_t zam_posit_to_int(ZamPosit value) {
    if (value == ZAMPOSIT_NAR)
        return INT64_MIN;
    if (value == 0)
        return 0;

    Unpacked unpacked = unpack(value);
    if (unpacked.scale < 0)
        return 0;
    if (unpacked.scale >= 63)
        return unpacked.negative ? INT64_MIN : INT64_MAX;
    uint64_t magnitude = unpacked.significand >> (63 - unpacked.scale);
    return unpacked.negative ? -(int64_t)magnitude : (int64_t)magnitude;
}

uint64_t zam_posit_to_uint(ZamPosit value) {
    if (value == 0 || (int64_t)value < 0)
        return 0;

    Unpacked unpacked = unpack(value);
    if (unpacked.scale < 0)
        return 0;
    if (unpacked.scale >= 64)
        return UINT64_MAX;
    return unpacked.significand >> (63 - unpacked.scale);
}

double zam_posit_to_double(ZamPosit value) {
    if (value == ZAMPOSIT_NAR)
        return NAN;
    if (value == 0)
        return 0;

    Unpacked unpacked = unpack(value);
    double magnitude = ldexp((double)unpacked.significand, unpacked.scale - 63);
    return unpacked.negative ? -magnitude : magnitude;
}

// Big is ±2^scale × significand / 2^127, the significand's leading one at bit
// 127, or zero when the significand is. It carries the elementary functions
// with more than twice the bits a posit has, so that rounding the result to
// one is wrong only for a value within about 2^-110 of a tie.
typedef struct Big {
    bool negative;
    int32_t scale;
    Wide significand;
} Big;

#define BIG_HIGH(high, low) (((Wide)(high) << 64) | (low))

static const Big BIG_ONE = {false, 0, (Wide)1 << 127};
static const Big BIG_PI_OVER_2 = {
    false, 0, BIG_HIGH(0xC90FDAA22168C234ULL, 0xC4C6628B80DC1CD1ULL)};
static const Big BIG_PI = {
    false, 1, BIG_HIGH(0xC90FDAA22168C234ULL, 0xC4C6628B80DC1CD1ULL)};
static const Big BIG_LN2 = {
    false, -1, BIG_HIGH(0xB17217F7D1CF79ABULL, 0xC9E3B39803F2F6AFULL)};
static const Big BIG_LN10 = {
    false, 1, BIG_HIGH(0x935D8DDDAAA8AC16ULL, 0xEA56D62B82D30A29ULL)};

// TWO_OVER_PI is 2/π as 64-bit limbs after the point, enough of them to
// reduce the largest posit.
static const uint64_t TWO_OVER_PI[] = {
    0xA2F9836E4E441529ULL, 0xFC2757D1F534DDC0ULL, 0xDB6295993C439041ULL,
    0xFE5163ABDEBBC561ULL, 0xB7246E3A424DD2E0ULL, 0x06492EEA09D1921CULL,
    0xFE1DEB1CB129A73EULL, 0xE88235F52EBB4484ULL, 0xE99C7026B45F7E41ULL,
    0x3991D639835339F4ULL,
};

// bigNormalize makes a Big of ±2^scale × significand / 2^127 for any nonzero
// significand, folding sticky into the lowest bit.
static Big bigNormalize(bool negative, int32_t scale, Wide significand,
                        bool sticky) {
    if (significand == 0)
        return (Big){0};
    int zeros = leadingZeros(significand);
    return (Big){negative, scale - zeros, (significand << zeros) | sticky};
}

static Big bigFromPosit(ZamPosit posit) {
    if (posit == 0)
        return (Big){0};
    Unpacked unpacked = unpack(posit);
    return (Big){unpacked.negative, unpacked.scale,
                 (Wide)unpacked.significand << 64};
}

static Big bigFromInt(int64_t value) {
    return bigNormalize(value < 0, 127,
                        value < 0 ? -(uint64_t)value : (uint64_t)value, false);
}

static Big bigFromDouble(double value) {
    if (value == 0)
        return (Big){0};
    int exponent;
    double fraction = frexp(fabs(value), &exponent);
    return bigNormalize(value < 0, exponent + 63,
                        (uint64_t)ldexp(fraction, 64), false);
}

static double bigToDouble(Big value) {
    if (value.significand == 0)
        return 0;
    double magnitude =
        ldexp((double)(uint64_t)(value.significand >> 64), value.scale - 63);
    return value.negative ? -magnitude : magnitude;
}

static ZamPosit bigToPosit(Big value) {
    if (value.significand == 0)
        return 0;
    return pack(value.negative, value.scale,
                (uint64_t)(value.significand >> 64),
                (uint64_t)value.significand != 0);
}

static Big bigNegate(Big value) {
    value.negative = !value.negative && value.significand != 0;
    return value;
}

static Big bigAbs(Big value) {
    value.negative = false;
    return value;
}

static Big bigScale(Big value, int32_t by) {
    if (value.significand != 0)
        value.scale += by;
    return value;
}

// bigLess reports whether |left| < |right|.
static bool bigLess(Big left, Big right) {
    if (left.significand == 0 || right.significand == 0)
        return right.significand != 0;
    return left.scale != right.scale ? left.scale < right.scale
                                     : left.significand < right.significand;
}

static Big bigAdd(Big left, Big right) {
    if (left.significand == 0)
        return right;
    if (right.significand == 0)
        return left;
    if (bigLess(left, right)) {
        Big swap = left;
        left = right;
        right = swap;
    }

    // Halved, with what falls off folded in, to leave room for a carry.
    Wide sum = (left.significand >> 1) | (left.significand & 1);
    Wide addend =
        shiftRightJam(right.significand, left.scale - right.scale + 1);
    sum = left.negative == right.negative ? sum + addend : sum - addend;
    return bigNormalize(left.negative, left.scale + 1, sum, false);
}

static Big bigSub(Big left, Big right) {
    return bigAdd(left, bigNegate(right));
}

static Big bigMul(Big left, Big right) {
    if (left.significand == 0 || right.significand == 0)
        return (Big){0};

    uint64_t a1 = (uint64_t)(left.significand >> 64);
    uint64_t a0 = (uint64_t)left.significand;
    uint64_t b1 = (uint64_t)(right.significand >> 64);
    uint64_t b0 = (uint64_t)right.significand;
    Wide low = (Wide)a0 * b0;
    Wide cross1 = (Wide)a0 * b1;
    Wide cross2 = (Wide)a1 * b0;
    Wide middle = (low >> 64) + (uint64_t)cross1 + (uint64_t)cross2;
    Wide high = (Wide)a1 * b1 + (cross1 >> 64) + (cross2 >> 64) + (middle >> 64);
    uint64_t below = (uint64_t)middle;
    bool sticky = (uint64_t)low != 0;

    bool negative = left.negative != right.negative;
    if (high >> 127)
        return (Big){negative, left.scale + right.scale + 1,
                     high | (below != 0 || sticky)};
    high = (high << 1) | (below >> 63);
    return (Big){negative, left.scale + right.scale,
                 high | ((below << 1) != 0 || sticky)};
}

static Big bigDiv(Big left, Big right) {
    if (left.significand == 0)
        return (Big){0};

    Wide remainder = left.significand;
    Wide divisor = right.significand;
    int32_t scale = left.scale - right.scale;
    Wide quotient = 0;
    int bits = 127;
    if (remainder >= divisor) {
        remainder -= divisor;
        quotient = 1;
    } else {
        scale--;
        bits = 128;
    }
    for (int bit = 0; bit < bits; bit++) {
        bool carry = remainder >> 127;
        remainder <<= 1;
        quotient <<= 1;
        if (carry || remainder >= divisor) {
            remainder -= divisor;
            quotient |= 1;
        }
    }
    return (Big){left.negative != right.negative, scale,
                 quotient | (remainder != 0)};
}

static Big bigDivInt(Big value, int64_t by) {
    return bigDiv(value, bigFromInt(by));
}

// bigSqrt is Newton's iteration from the double's square root, each step
// doubling the bits that are right.
static Big bigSqrt(Big value) {
    if (value.significand == 0)
        return value;
    int32_t half = value.scale >> 1;
    Big reduced = bigScale(value, -2 * half);
    Big root = bigFromDouble(sqrt(bigToDouble(reduced)));
    for (int step = 0; step < 3; step++)
        root = bigScale(bigAdd(root, bigDiv(reduced, root)), -1);
    return bigScale(root, half);
}

// negligible reports whether adding term to sum no longer changes it.
static bool negligible(Big term, Big sum) {
    return term.significand == 0 ||
           (sum.significand != 0 && term.scale < sum.scale - 130);
}

// bigExp reduces x by multiples of ln 2 and then by 2^12, sums the series,
// and squares back. Beyond ±200 it answers a value only the posit's range
// saturates.
static Big bigExp(Big x) {
    double approximate = bigToDouble(x);
    if (approximate > 200)
        return (Big){false, 300, (Wide)1 << 127};
    if (approximate < -200)
        return (Big){false, -300, (Wide)1 << 127};

    int64_t k = (int64_t)nearbyint(approximate * 1.4426950408889634);
    Big r = bigScale(bigSub(x, bigMul(bigFromInt(k), BIG_LN2)), -12);
    Big sum = BIG_ONE;
    Big term = BIG_ONE;
    for (int64_t n = 1; !negligible(term, sum); n++) {
        term = bigDivInt(bigMul(term, r), n);
        sum = bigAdd(sum, term);
    }
    for (int square = 0; square < 12; square++)
        sum = bigMul(sum, sum);
    return bigScale(sum, (int32_t)k);
}

// bigExpm1 sums e^x - 1 directly near zero, where subtracting would cancel.
static Big bigExpm1(Big x) {
    if (bigLess(x, (Big){false, -1, (Wide)1 << 127})) {
        Big sum = x;
        Big term = x;
        for (int64_t n = 2; !negligible(term, sum); n++) {
            term = bigDivInt(bigMul(term, x), n);
            sum = bigAdd(sum, term);
        }
        return sum;
    }
    return bigSub(bigExp(x), BIG_ONE);
}

// bigAtanhSeries sums atanh(t) = t + t^3/3 + t^5/5 + …, for small t.
static Big bigAtanhSeries(Big t) {
    Big square = bigMul(t, t);
    Big sum = t;
    Big power = t;
    for (int64_t n = 3;; n += 2) {
        power = bigMul(power, square);
        Big term = bigDivInt(power, n);
        if (negligible(term, sum))
            return sum;
        sum = bigAdd(sum, term);
    }
}

// bigLog splits x into 2^e × m with m within [√½, √2), and takes log m as
// 2 atanh((m - 1) / (m + 1)).
static Big bigLog(Big x) {
    int32_t exponent = x.scale;
    Big m = {false, 0, x.significand};
    if ((uint64_t)(x.significand >> 64) >= 0xB504F333F9DE6484ULL) {
        m.scale = -1;
        exponent++;
    }
    Big t = bigDiv(bigSub(m, BIG_ONE), bigAdd(m, BIG_ONE));
    Big log = bigScale(bigAtanhSeries(t), 1);
    return bigAdd(bigMul(bigFromInt(exponent), BIG_LN2), log);
}

// bigLog1p takes log(1 + x) as 2 atanh(x / (2 + x)) near zero, without
// forming 1 + x.
static Big bigLog1p(Big x) {
    if (bigLess(x, (Big){false, -1, (Wide)1 << 127})) {
        Big t = bigDiv(x, bigAdd(bigFromInt(2), x));
        return bigScale(bigAtanhSeries(t), 1);
    }
    return bigLog(bigAdd(BIG_ONE, x));
}

// bigAtan reduces |x| to at most 1 by atan x = π/2 - atan(1/x), then halves
// the angle three times by atan x = 2 atan(x / (1 + √(1 + x²))) before the
// series.
static Big bigAtan(Big x) {
    if (x.significand == 0)
        return x;
    bool negative = x.negative;
    x = bigAbs(x);
    bool inverted = bigLess(BIG_ONE, x);
    if (inverted)
        x = bigDiv(BIG_ONE, x);
    for (int halving = 0; halving < 3; halving++)
        x = bigDiv(x, bigAdd(BIG_ONE, bigSqrt(bigAdd(BIG_ONE, bigMul(x, x)))));

    Big square = bigMul(x, x);
    Big sum = x;
    Big power = x;
    for (int64_t n = 3;; n += 2) {
        power = bigNegate(bigMul(power, square));
        Big term = bigDivInt(power, n);
        if (negligible(term, sum))
            break;
        sum = bigAdd(sum, term);
    }
    sum = bigScale(sum, 3);
    if (inverted)
        sum = bigSub(BIG_PI_OVER_2, sum);
    return negative ? bigNegate(sum) : sum;
}

// bigSinCos sums the series of sin r, or of cos r, for |r| ≤ π/4.
static Big bigSinCos(Big r, bool cosine) {
    Big square = bigMul(r, r);
    Big term = cosine ? BIG_ONE : r;
    Big sum = term;
    for (int64_t n = cosine ? 1 : 2;; n += 2) {
        term = bigNegate(bigDivInt(bigMul(term, square), n * (n + 1)));
        if (negligible(term, sum))
            return sum;
        sum = bigAdd(sum, term);
    }
}

// reduce answers r and n with |x| = n π/2 + r and |r| ≤ π/4, n taken modulo
// 4. Past π/4 it multiplies the significand by just the window of 2/π's bits
// that lands between the units digit and far enough below it for the closest
// a posit comes to a multiple of π/2.
static Big reduce(ZamPosit x, int *quadrant) {
    Unpacked unpacked = unpack(x);
    *quadrant = 0;
    if (unpacked.scale < -1)
        return (Big){false, unpacked.scale,
                     (Wide)unpacked.significand << 64};

    int32_t exponent = unpacked.scale - 63;
    int32_t first = exponent >= 66 ? (exponent - 66) / 64 + 1 : 0;
    int32_t base = exponent - 64 * first - 64;

    // product is the significand times six limbs of 2/π, seven limbs most
    // significant first, worth product × 2^(base - 320).
    uint64_t product[7] = {0};
    Wide carry = 0;
    for (int limb = 5; limb >= 0; limb--) {
        Wide part = (Wide)unpacked.significand * TWO_OVER_PI[first + limb] + carry;
        product[limb + 1] = (uint64_t)part;
        carry = part >> 64;
    }
    product[0] = (uint64_t)carry;

    int fractionBits = 320 - base;
#define BIT(index)                                                            \
    ((index) >= 448 ? 0 : (product[6 - (index) / 64] >> ((index) % 64)) & 1)
    *quadrant = (int)(BIT(fractionBits) | (BIT(fractionBits + 1) << 1));
    bool negative = BIT(fractionBits - 1);
    for (int index = fractionBits; index < 448; index++)
        product[6 - index / 64] &= ~((uint64_t)1 << (index % 64));
    if (negative) {
        // The fraction is at least ½, so r is measured back from the next
        // multiple, 1 - fraction.
        (*quadrant)++;
        bool borrow = true;
        for (int limb = 6; limb >= 0; limb--) {
            product[limb] = ~product[limb] + borrow;
            borrow = borrow && product[limb] == 0;
        }
        for (int index = fractionBits; index < 448; index++)
            product[6 - index / 64] &= ~((uint64_t)1 << (index % 64));
    }
    *quadrant &= 3;

    int top = fractionBits - 1;
    while (top >= 0 && !BIT(top))
        top--;
    if (top < 0)
        return (Big){0};
    Wide significand = 0;
    bool sticky = false;
    for (int index = top; index > top - 128; index--)
        significand = (significand << 1) | (index >= 0 ? BIT(index) : 0);
    for (int index = top - 128; index >= 0 && !sticky; index--)
        sticky = BIT(index);
#undef BIT
    Big fraction = {negative, top - fractionBits, significand | sticky};
    return bigMul(fraction, BIG_PI_OVER_2);
}

// sinCos answers sin x, or cos x, of a real posit.
static Big sinCos(ZamPosit x, bool cosine) {
    bool negative = (int64_t)x < 0;
    int quadrant;
    Big r = reduce(negative ? -x : x, &quadrant);
    if (cosine)
        quadrant++;
    Big value = bigSinCos(r, quadrant & 1);
    if (quadrant & 2)
        value = bigNegate(value);
    return negative && !cosine ? bigNegate(value) : value;
}

#define UNARY(name, body)                                                     \
    ZamPosit name(ZamPosit x) {                                               \
        if (x == ZAMPOSIT_NAR)                                                \
            return ZAMPOSIT_NAR;                                              \
        body                                                                  \
    }

#define BINARY(name, body)                                                    \
    ZamPosit name(ZamPosit x, ZamPosit y) {                                   \
        if (x == ZAMPOSIT_NAR || y == ZAMPOSIT_NAR)                           \
            return ZAMPOSIT_NAR;                                              \
        body                                                                  \
    }

static bool isNegative(ZamPosit x) {
    return (int64_t)x < 0;
}

// isqrt answers the integer square root of n, and whether it was exact.
static uint64_t isqrt(Wide n, bool *inexact) {
    Wide remainder = 0;
    Wide root = 0;
    for (int bit = 0; bit < 64; bit++) {
        root <<= 1;
        remainder = (remainder << 2) | (n >> 126);
        n <<= 2;
        Wide trial = (root << 1) | 1;
        if (remainder >= trial) {
            remainder -= trial;
            root |= 1;
        }
    }
    *inexact = remainder != 0;
    return (uint64_t)root;
}

UNARY(zam_posit_sqrt, {
    if (isNegative(x))
        return ZAMPOSIT_NAR;
    if (x == 0)
        return 0;
    Unpacked unpacked = unpack(x);
    bool odd = unpacked.scale & 1;
    Wide radicand = (Wide)unpacked.significand << (odd ? 64 : 63);
    int32_t exponent = (unpacked.scale - (odd ? 127 : 126)) / 2;
    bool inexact;
    uint64_t root = isqrt(radicand, &inexact);
    return pack(false, exponent + 63, root, inexact);
})

UNARY(zam_posit_cbrt, {
    if (x == 0)
        return 0;
    Big value = bigFromPosit(x);
    Big root = bigExp(bigDivInt(bigLog(bigAbs(value)), 3));
    return bigToPosit(value.negative ? bigNegate(root) : root);
})

UNARY(zam_posit_exp, { return bigToPosit(bigExp(bigFromPosit(x))); })

UNARY(zam_posit_exp2, {
    Big value = bigFromPosit(x);
    double approximate = bigToDouble(value);
    if (approximate > 300)
        return MAXPOS;
    if (approximate < -300)
        return MINPOS;
    int64_t whole = (int64_t)floor(approximate);
    Big fraction = bigSub(value, bigFromInt(whole));
    if (fraction.negative) {
        whole--;
        fraction = bigAdd(fraction, BIG_ONE);
    }
    return bigToPosit(
        bigScale(bigExp(bigMul(fraction, BIG_LN2)), (int32_t)whole));
})

UNARY(zam_posit_expm1, { return bigToPosit(bigExpm1(bigFromPosit(x))); })

UNARY(zam_posit_log, {
    if (x == 0 || isNegative(x))
        return ZAMPOSIT_NAR;
    return bigToPosit(bigLog(bigFromPosit(x)));
})

UNARY(zam_posit_log2, {
    if (x == 0 || isNegative(x))
        return ZAMPOSIT_NAR;
    return bigToPosit(bigDiv(bigLog(bigFromPosit(x)), BIG_LN2));
})

UNARY(zam_posit_log10, {
    if (x == 0 || isNegative(x))
        return ZAMPOSIT_NAR;
    return bigToPosit(bigDiv(bigLog(bigFromPosit(x)), BIG_LN10));
})

UNARY(zam_posit_log1p, {
    Big value = bigFromPosit(x);
    if (value.negative && !bigLess(value, BIG_ONE))
        return ZAMPOSIT_NAR;
    return bigToPosit(bigLog1p(value));
})

// rounded answers x rounded to a whole number toward zero, then stepped away
// from zero by one where away says to from whether the dropped fraction was
// nonzero and whether it was at least a half.
static ZamPosit rounded(ZamPosit x,
                        bool (*away)(bool negative, bool fraction, bool half)) {
    if (x == 0)
        return 0;
    Unpacked unpacked = unpack(x);
    if (unpacked.scale >= 63)
        return x;
    uint64_t whole = 0;
    bool fraction = true;
    bool half = false;
    if (unpacked.scale >= 0) {
        int dropped = 63 - unpacked.scale;
        whole = unpacked.significand >> dropped;
        fraction = (unpacked.significand << (64 - dropped)) != 0;
        half = (unpacked.significand >> (dropped - 1)) & 1;
    } else {
        half = unpacked.scale == -1;
    }
    if (away(unpacked.negative, fraction, half))
        whole++;
    return fromMagnitude(unpacked.negative, whole);
}

static bool awayForFloor(bool negative, bool fraction, bool half) {
    (void)half;
    return negative && fraction;
}

static bool awayForCeil(bool negative, bool fraction, bool half) {
    (void)half;
    return !negative && fraction;
}

static bool awayForRound(bool negative, bool fraction, bool half) {
    (void)negative;
    (void)fraction;
    return half;
}

static bool awayForTrunc(bool negative, bool fraction, bool half) {
    (void)negative;
    (void)fraction;
    (void)half;
    return false;
}

UNARY(zam_posit_floor, { return rounded(x, awayForFloor); })
UNARY(zam_posit_ceil, { return rounded(x, awayForCeil); })
UNARY(zam_posit_round, { return rounded(x, awayForRound); })
UNARY(zam_posit_trunc, { return rounded(x, awayForTrunc); })

// modulo answers |x| - n|y| for the whole n leaving it below |y|, signed as
// x, exactly before rounding, and the parity of n. Repeated doubling carries
// the remainder down the exponent gap one bit at a time.
static ZamPosit modulo(ZamPosit x, ZamPosit y, bool nearest) {
    if (y == 0)
        return ZAMPOSIT_NAR;
    if (x == 0)
        return 0;
    Unpacked dividend = unpack(x);
    Unpacked divisor = unpack(y);
    Wide modulus = divisor.significand;
    int32_t gap = dividend.scale - divisor.scale;
    Wide remainder = dividend.significand;
    bool odd = false;
    if (gap < 0) {
        if (!nearest)
            return x;
        // n is 0, or 1 where |x| is more than half |y|.
        if (gap == -1 && dividend.significand > divisor.significand) {
            Big difference = bigSub(bigAbs(bigFromPosit(y)),
                                    bigAbs(bigFromPosit(x)));
            return bigToPosit(dividend.negative ? difference
                                                : bigNegate(difference));
        }
        return x;
    }
    if (remainder >= modulus) {
        remainder -= modulus;
        odd = true;
    }
    for (int32_t step = 0; step < gap; step++) {
        remainder <<= 1;
        odd = remainder >= modulus;
        if (odd)
            remainder -= modulus;
    }

    bool negative = dividend.negative;
    if (nearest && (2 * remainder > modulus ||
                    (2 * remainder == modulus && odd))) {
        remainder = modulus - remainder;
        negative = !negative;
    }
    if (remainder == 0)
        return 0;
    Big value = bigNormalize(negative, divisor.scale + 64, remainder, false);
    return bigToPosit(value);
}

BINARY(zam_posit_mod, { return modulo(x, y, false); })
BINARY(zam_posit_remainder, { return modulo(x, y, true); })

static bool isWhole(ZamPosit x) {
    return zam_posit_floor(x) == x;
}

// isOdd reports whether a whole x is odd. Past 2^59 a posit has no bits left
// for the units digit, so it is even.
static bool isOdd(ZamPosit x) {
    return unpack(x).scale < 60 && (zam_posit_to_int(x) & 1);
}

BINARY(zam_posit_pow, {
    if (y == 0 || x == ZAMPOSIT_ONE)
        return ZAMPOSIT_ONE;
    if (x == 0)
        return isNegative(y) ? ZAMPOSIT_NAR : 0;
    bool negative = false;
    if (isNegative(x)) {
        if (!isWhole(y))
            return ZAMPOSIT_NAR;
        negative = isOdd(y);
        x = -x;
    }
    Big value =
        bigExp(bigMul(bigFromPosit(y), bigLog(bigFromPosit(x))));
    return bigToPosit(negative ? bigNegate(value) : value);
})

BINARY(zam_posit_hypot, {
    Big a = bigFromPosit(x);
    Big b = bigFromPosit(y);
    return bigToPosit(bigSqrt(bigAdd(bigMul(a, a), bigMul(b, b))));
})

ZamPosit zam_posit_fma(ZamPosit x, ZamPosit y, ZamPosit z) {
    if (x == ZAMPOSIT_NAR || y == ZAMPOSIT_NAR || z == ZAMPOSIT_NAR)
        return ZAMPOSIT_NAR;
    return bigToPosit(
        bigAdd(bigMul(bigFromPosit(x), bigFromPosit(y)), bigFromPosit(z)));
}

UNARY(zam_posit_sin, {
    if (x == 0)
        return 0;
    return bigToPosit(sinCos(x, false));
})

UNARY(zam_posit_cos, { return bigToPosit(sinCos(x, true)); })

UNARY(zam_posit_tan, {
    if (x == 0)
        return 0;
    return bigToPosit(bigDiv(sinCos(x, false), sinCos(x, true)));
})

UNARY(zam_posit_asin, {
    Big value = bigFromPosit(x);
    int order = bigLess(value, BIG_ONE) ? -1 : bigLess(BIG_ONE, value) ? 1 : 0;
    if (order > 0)
        return ZAMPOSIT_NAR;
    if (order == 0)
        return bigToPosit(value.negative ? bigNegate(BIG_PI_OVER_2)
                                         : BIG_PI_OVER_2);
    Big root = bigSqrt(
        bigMul(bigSub(BIG_ONE, value), bigAdd(BIG_ONE, value)));
    return bigToPosit(bigAtan(bigDiv(value, root)));
})

UNARY(zam_posit_acos, {
    Big value = bigFromPosit(x);
    if (bigLess(BIG_ONE, value))
        return ZAMPOSIT_NAR;
    if (value.negative && !bigLess(value, BIG_ONE))
        return bigToPosit(BIG_PI);
    Big ratio = bigDiv(bigSub(BIG_ONE, value), bigAdd(BIG_ONE, value));
    return bigToPosit(bigScale(bigAtan(bigSqrt(ratio)), 1));
})

UNARY(zam_posit_atan, { return bigToPosit(bigAtan(bigFromPosit(x))); })

ZamPosit zam_posit_atan2(ZamPosit y, ZamPosit x) {
    if (x == ZAMPOSIT_NAR || y == ZAMPOSIT_NAR)
        return ZAMPOSIT_NAR;
    if (x == 0) {
        if (y == 0)
            return 0;
        return bigToPosit(isNegative(y) ? bigNegate(BIG_PI_OVER_2)
                                        : BIG_PI_OVER_2);
    }
    Big angle = bigAtan(bigAbs(bigDiv(bigFromPosit(y), bigFromPosit(x))));
    if (isNegative(x))
        angle = bigSub(BIG_PI, angle);
    return bigToPosit(isNegative(y) ? bigNegate(angle) : angle);
}

UNARY(zam_posit_sinh, {
    Big value = bigFromPosit(x);
    Big m = bigExpm1(bigAbs(value));
    Big sinh = bigScale(bigAdd(m, bigDiv(m, bigAdd(m, BIG_ONE))), -1);
    return bigToPosit(value.negative ? bigNegate(sinh) : sinh);
})

UNARY(zam_posit_cosh, {
    Big e = bigExp(bigAbs(bigFromPosit(x)));
    return bigToPosit(bigScale(bigAdd(e, bigDiv(BIG_ONE, e)), -1));
})

UNARY(zam_posit_tanh, {
    Big value = bigFromPosit(x);
    if (bigToDouble(bigAbs(value)) > 50)
        return value.negative ? -ZAMPOSIT_ONE : ZAMPOSIT_ONE;
    Big m = bigExpm1(bigScale(bigAbs(value), 1));
    Big tanh = bigDiv(m, bigAdd(m, bigFromInt(2)));
    return bigToPosit(value.negative ? bigNegate(tanh) : tanh);
})

UNARY(zam_posit_asinh, {
    Big value = bigFromPosit(x);
    Big a = bigAbs(value);
    Big square = bigMul(a, a);
    Big root = bigSqrt(bigAdd(BIG_ONE, square));
    Big asinh = bigLog1p(bigAdd(a, bigDiv(square, bigAdd(BIG_ONE, root))));
    return bigToPosit(value.negative ? bigNegate(asinh) : asinh);
})

UNARY(zam_posit_acosh, {
    Big value = bigFromPosit(x);
    if (value.negative || bigLess(value, BIG_ONE))
        return ZAMPOSIT_NAR;
    Big t = bigSub(value, BIG_ONE);
    Big root = bigSqrt(bigAdd(bigScale(t, 1), bigMul(t, t)));
    return bigToPosit(bigLog1p(bigAdd(t, root)));
})

UNARY(zam_posit_atanh, {
    Big value = bigFromPosit(x);
    if (!bigLess(bigAbs(value), BIG_ONE))
        return ZAMPOSIT_NAR;
    Big ratio = bigDiv(bigScale(value, 1), bigSub(BIG_ONE, value));
    return bigToPosit(bigScale(bigLog1p(ratio), -1));
})
