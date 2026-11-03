BIN := bin/zam
SRC := $(wildcard src/*.c)
OBJ := $(SRC:src/%.c=build/%.o)

CODEC_PACKAGES := json-c yaml-0.1 libxml-2.0 bson2 libcbor zlib liblz4 libzstd libbrotlienc libbrotlidec openssl libcurl libllhttp libsodium libxcrypt
CODEC_KEGS := $(subst $(eval) ,:,$(wildcard /opt/homebrew/opt/curl/lib/pkgconfig /usr/local/opt/curl/lib/pkgconfig /opt/homebrew/opt/libxcrypt/lib/pkgconfig /usr/local/opt/libxcrypt/lib/pkgconfig))
PKG_CONFIG := PKG_CONFIG_PATH=$(CODEC_KEGS):$$PKG_CONFIG_PATH pkg-config

CFLAGS ?= -std=gnu11 -Wall -Wextra -Wpedantic -Werror -Iinclude -Isrc $(shell $(PKG_CONFIG) --cflags $(CODEC_PACKAGES))
LDFLAGS ?= -lpthread -lm $(shell $(PKG_CONFIG) --libs $(CODEC_PACKAGES)) -larchive

# The default build is a debug build. Nothing here is fast yet and everything
# here is half-built, so a run that trips an assertion is worth more than a run
# that is quick about it.
CFLAGS += -g -O0

all: $(BIN)

$(BIN): $(OBJ)
	@mkdir -p $(dir $@)
	$(CC) $(OBJ) $(LDFLAGS) -o $@

build/%.o: src/%.c
	@mkdir -p $(dir $@)
	$(CC) $(CFLAGS) -MMD -MP -c $< -o $@

-include $(OBJ:.o=.d)

clean:
	rm -rf build $(BIN)

.PHONY: all clean
