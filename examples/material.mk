# Material 3 build pipeline: AAR closure resource compilation, R.java
# generation, multi-AAR classpath, native multidex. Activated by setting
# EXAMPLE_USES_MATERIAL := true BEFORE `include ../apk.mk`.
#
# Inputs (per example):
#   res/                                 Example resources (layouts, themes).
#   $(EXAMPLE_NAME) Makefile             EXAMPLE_NAME, optional EXAMPLE_PERMISSIONS.
# Inputs (shared, repo root):
#   .aar-cache/lock.json                 Cycle-2 deterministic AAR closure.
#   .aar-cache/<group>/<artifact>/...    Cached AAR/JAR artifacts.
#
# Outputs (per example):
#   $(BUILD)/$(EXAMPLE_NAME).apk         Final signed APK with Material 3 deps.
#
# Side effects (shared, idempotent):
#   .aar-cache/extracted/<group>/<artifact>/<version>/{classes.jar,res/}
#   .aar-cache/compiled/<group>/<artifact>/<version>/*.flat

REPO_ROOT       := $(abspath $(dir $(lastword $(MAKEFILE_LIST)))/..)
LOCK_FILE       := $(REPO_ROOT)/.aar-cache/lock.json
AAR_CACHE       := $(REPO_ROOT)/.aar-cache
LOCK_PATHS      := $(REPO_ROOT)/tools/cmd/lock-paths
APP_COMPILED    := $(BUILD)/app-compiled
GEN_DIR         := $(BUILD)/gen
RTXT            := $(BUILD)/R.txt
EXAMPLE_THEME_REF ?= @style/Theme.App
EXAMPLE_NEEDS_PROXY := true

# Resource files the example ships under ./res. aapt2 only needs to know they
# exist for dependency tracking; the actual compile glob happens at recipe time.
EXAMPLE_RES_FILES := $(shell find res -type f 2>/dev/null)

# AAR closure stamps. The lock file pins everything, so any change to it
# invalidates extract; extract invalidates compile; compile invalidates link.
$(BUILD)/.material-extracted: $(LOCK_FILE)
	@mkdir -p $(BUILD)
	cd $(REPO_ROOT) && go run ./tools/cmd/lock-paths --lock $(LOCK_FILE) --cache $(AAR_CACHE) --extract
	@touch $@

$(BUILD)/.material-compiled: $(BUILD)/.material-extracted
	cd $(REPO_ROOT) && go run ./tools/cmd/lock-paths --lock $(LOCK_FILE) --cache $(AAR_CACHE) --compile --aapt2 $(AAPT2)
	@touch $@

# Compile the example's own resources. aapt2 emits one .flat per source file.
$(APP_COMPILED)/.stamp: $(EXAMPLE_RES_FILES)
	@mkdir -p $(APP_COMPILED)
	$(AAPT2) compile --dir res -o $(APP_COMPILED)
	@touch $@

# AAR-derived classpath (every classes.jar in the resolved closure) and the
# space-separated -R flat-arg list. Both are evaluated lazily at recipe time
# so they reflect the post-extract / post-compile state.
AAR_CLASSPATH = $(shell cd $(REPO_ROOT) && go run ./tools/cmd/lock-paths --lock $(LOCK_FILE) --cache $(AAR_CACHE) --field classes-jars)
AAR_FLAT_ARGS = $(shell cd $(REPO_ROOT) && go run ./tools/cmd/lock-paths --lock $(LOCK_FILE) --cache $(AAR_CACHE) --field flat-args)
APP_FLAT_ARGS = $(addprefix -R ,$(wildcard $(APP_COMPILED)/*.flat))

# kotlin-stdlib-jdk7 and kotlin-stdlib-jdk8 were folded into kotlin-stdlib in
# Kotlin 1.8; the legacy artifacts duplicate every class. Drop them from the
# d8 input set (keeping them on the javac classpath is fine because javac
# resolves the first match). Make's filter-out patterns only allow one `%`,
# so the filter is implemented via shell grep.
DEXED_AAR_CLASSPATH = $(shell echo $(AAR_CLASSPATH) | tr ' ' '\n' | grep -v -E '/kotlin-stdlib-jdk[78]/' | tr '\n' ' ')

# Build a colon-separated -I list for aapt2 link (one -I per classes.jar plus
# android.jar). aapt2's -I flag does not accept a colon-joined list; it must
# be repeated.
JAR_INCLUDES = -I $(PLATFORM)/android.jar $(addprefix -I ,$(AAR_CLASSPATH))

# AAR packages whose R.java aapt2 must also emit so each AAR's <clinit>
# (which references its own R$style/R$id constants) can resolve at runtime.
# Extracted from each AAR's AndroidManifest.xml at recipe time.
EXTRA_PACKAGES = $(shell find $(AAR_CACHE) -name '*.aar' 2>/dev/null | while read aar; do unzip -p "$$aar" AndroidManifest.xml 2>/dev/null | grep -oP 'package="\K[^"]+'; done | sort -u | tr '\n' ':' | sed 's/:$$//')

# aapt2 link consumes every AAR's compiled flats plus the example's flats,
# emits R.java for the example's package, and writes base.apk with merged
# resources. Manifest merging is intentionally minimal: aapt2 link --manifest
# accepts the example's NativeActivity manifest as-is; the AAR manifests
# contribute only resource symbols, not <application> children, which is what
# we want for a NativeActivity-rooted app.
$(BUILD)/base-resources.apk $(GEN_DIR)/.stamp: $(BUILD)/AndroidManifest.xml $(BUILD)/.material-compiled $(APP_COMPILED)/.stamp
	@mkdir -p $(GEN_DIR)
	$(AAPT2) link --manifest $(BUILD)/AndroidManifest.xml \
		--auto-add-overlay \
		--extra-packages $(EXTRA_PACKAGES) \
		$(JAR_INCLUDES) \
		--min-sdk-version $(MIN_SDK) \
		--target-sdk-version $(TARGET_SDK) \
		--java $(GEN_DIR) \
		--output-text-symbols $(RTXT) \
		$(AAR_FLAT_ARGS) $(APP_FLAT_ARGS) \
		-o $(BUILD)/base-resources.apk
	@touch $(GEN_DIR)/.stamp

# javac compiles the proxy-dispatch shims plus the generated R.java against
# the full AAR classpath. d8 with --min-api $(MIN_SDK) emits native multidex
# (multiple classes*.dex files) when the dex method count overflows.
$(BUILD)/classes.dex: $(GEN_DIR)/.stamp $(HANDLER_JAVA) $(DISPATCH_JAVA) $(EXAMPLE_EXTRA_JAVA)
	@mkdir -p $(BUILD)/java
	@CP="$(PLATFORM)/android.jar"; for j in $(AAR_CLASSPATH); do CP="$$CP:$$j"; done; \
		javac --release 17 -classpath "$$CP" \
			-d $(BUILD)/java '$(HANDLER_JAVA)' '$(DISPATCH_JAVA)' $(foreach src,$(EXAMPLE_EXTRA_JAVA),'$(src)') \
			$$(find $(GEN_DIR) -name '*.java')
	@D8_LIBS=""; for j in $(DEXED_AAR_CLASSPATH); do D8_LIBS="$$D8_LIBS --classpath $$j"; done; \
		$(D8) --min-api $(MIN_SDK) --lib $(PLATFORM)/android.jar $$D8_LIBS \
			--output $(BUILD) \
			$$(find $(BUILD)/java -name '*.class') \
			$(DEXED_AAR_CLASSPATH)

# Override the apk.mk APK rule: build from base-resources.apk so the link
# step's resources are honoured, then zip in classes*.dex and native libs.
$(BUILD)/$(EXAMPLE_NAME).apk: $(BUILD)/base-resources.apk $(BUILD)/classes.dex $(BUILD)/lib/arm64-v8a/libexample.so $(BUILD)/lib/x86_64/libexample.so $(BUILD)/debug.keystore
	cp $(BUILD)/base-resources.apk $(BUILD)/base.apk
	cd $(BUILD) && zip -j base.apk $$(ls classes*.dex)
	cd $(BUILD) && zip -r base.apk lib/
	$(ZIPALIGN) -f 4 $(BUILD)/base.apk $(BUILD)/aligned.apk
	$(APKSIGNER) sign --ks $(BUILD)/debug.keystore --ks-pass pass:android \
		--out $@ $(BUILD)/aligned.apk
	@rm -f $(BUILD)/base.apk $(BUILD)/aligned.apk
	@echo "Built: $@"
