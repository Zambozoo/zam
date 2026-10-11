BIN := bin/zam
SRC := $(wildcard src/*.c)
OBJ := $(SRC:src/%.c=build/%.o)

CFLAGS ?= -std=gnu11 -Wall -Wextra -Wpedantic -Werror -Iinclude -Isrc
LDFLAGS ?= -lpthread

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
