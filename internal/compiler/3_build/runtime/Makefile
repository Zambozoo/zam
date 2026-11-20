SRC := $(wildcard src/*.c)
OBJ := $(SRC:src/%.c=build/%.o)

# The runtime is compiled with Zig for the host, against the host's target pack
# from $ZAM_PACKS or the packs built into bin/packs, as a native build does.
OS := $(subst Darwin,macos,$(subst Linux,linux-musl,$(shell uname -s)))
ARCH := $(subst arm64,aarch64,$(shell uname -m))
TARGET ?= $(ARCH)-$(OS)
ZIG_TARGET := $(if $(filter macos,$(OS)),$(TARGET).13.0,$(TARGET))
PACKS ?= $(or $(ZAM_PACKS),$(abspath ../../../../bin/packs))
PACK := $(PACKS)/$(TARGET)
ifeq ($(filter clean,$(MAKECMDGOALS)),)
ifeq ($(wildcard $(PACK)/sources.txt),)
$(error no $(TARGET) pack in $(PACKS); run task packs or set ZAM_PACKS)
endif
endif

CC := zig cc
CFLAGS ?= -target $(ZIG_TARGET) -std=gnu11 -Wall -Wextra -Wpedantic -Werror -Iinclude -Isrc \
	-DCURL_STATICLIB -DBSON_STATIC -DLIBXML_STATIC \
	-I$(PACK)/include -I$(PACK)/include/libxml2 $(addprefix -I,$(wildcard $(PACK)/include/bson-*))

# The default build is a debug build. Nothing here is fast yet and everything
# here is half-built, so a run that trips an assertion is worth more than a run
# that is quick about it.
CFLAGS += -g -O0

all: $(OBJ)

build/%.o: src/%.c
	@mkdir -p $(dir $@)
	$(CC) $(CFLAGS) -MMD -MP -c $< -o $@

-include $(OBJ:.o=.d)

clean:
	rm -rf build

.PHONY: all clean
