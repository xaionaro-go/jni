# Shared APK build rules for JNI examples using NativeActivity.
ANDROID_SDK ?= $(shell \
	if [ -n "$$ANDROID_HOME" ]; then echo "$$ANDROID_HOME"; \
	elif [ -d "$$HOME/Android/Sdk" ]; then echo "$$HOME/Android/Sdk"; \
	elif [ -d "$$HOME/android-sdk" ]; then echo "$$HOME/android-sdk"; \
	fi)
NDK_VERSION  ?= $(shell ls $(ANDROID_SDK)/ndk 2>/dev/null | sort -V | tail -1)
NDK          ?= $(ANDROID_SDK)/ndk/$(NDK_VERSION)
BUILD_TOOLS  ?= $(shell ls $(ANDROID_SDK)/build-tools 2>/dev/null | sort -V | tail -1)
PLATFORM     ?= $(shell ls -d $(ANDROID_SDK)/platforms/android-* 2>/dev/null | sort -V | tail -1)
MIN_SDK      ?= 24
TARGET_SDK   ?= $(shell basename $(PLATFORM) | sed 's/android-//')
ADB       := $(ANDROID_SDK)/platform-tools/adb
AAPT2     := $(ANDROID_SDK)/build-tools/$(BUILD_TOOLS)/aapt2
D8        := $(ANDROID_SDK)/build-tools/$(BUILD_TOOLS)/d8
ZIPALIGN  := $(ANDROID_SDK)/build-tools/$(BUILD_TOOLS)/zipalign
APKSIGNER := $(ANDROID_SDK)/build-tools/$(BUILD_TOOLS)/apksigner
space := $(subst ,, )
comma := ,
NDK_TOOLCHAIN := $(NDK)/toolchains/llvm/prebuilt/linux-x86_64/bin
CC_ARM64 := $(NDK_TOOLCHAIN)/aarch64-linux-android$(MIN_SDK)-clang
CC_AMD64 := $(NDK_TOOLCHAIN)/x86_64-linux-android$(MIN_SDK)-clang
BUILD        := build
HANDLER_DIR  := ../../internal/testjvm/testdata
PACKAGE_NAME ?= center.dx.jni.examples.$(EXAMPLE_NAME)
.PHONY: all build install run clean
all: build
build: $(BUILD)/$(EXAMPLE_NAME).apk
$(BUILD)/debug.keystore:
	@mkdir -p $(BUILD)
	keytool -genkeypair -keystore $@ -storepass android -alias debug \
		-keyalg RSA -keysize 2048 -validity 10000 \
		-dname "CN=Debug" -noprompt 2>/dev/null
$(BUILD)/AndroidManifest.xml:
	@mkdir -p $(BUILD)
	@printf '<?xml version="1.0" encoding="utf-8"?>\n' > $@
	@printf '<manifest xmlns:android="http://schemas.android.com/apk/res/android"\n' >> $@
	@printf '    package="%s">\n' '$(PACKAGE_NAME)' >> $@
	@$(foreach perm,$(EXAMPLE_PERMISSIONS), \
		printf '    <uses-permission android:name="%s" />\n' '$(perm)' >> $@;)
	@printf '    <application android:label="%s" android:hasCode="%s" android:debuggable="true"' '$(EXAMPLE_NAME)' '$(if $(filter true,$(EXAMPLE_NEEDS_PROXY)),true,false)' >> $@
	@if [ -n '$(EXAMPLE_THEME_REF)' ]; then printf ' android:theme="%s"' '$(EXAMPLE_THEME_REF)' >> $@; fi
	@printf '>\n' >> $@
	@PERM_CSV="$(subst $(space),$(comma),$(EXAMPLE_PERMISSIONS))"; \
		if [ -n "$$PERM_CSV" ]; then \
			printf '        <meta-data android:name="example.permissions" android:value="%s" />\n' "$$PERM_CSV" >> $@; \
		fi
	@printf '        <activity android:name="android.app.NativeActivity"\n' >> $@
	@printf '                  android:exported="true"\n' >> $@
	@printf '                  android:configChanges="orientation|keyboardHidden">\n' >> $@
	@printf '            <meta-data android:name="android.app.lib_name" android:value="example" />\n' >> $@
	@printf '            <intent-filter>\n' >> $@
	@printf '                <action android:name="android.intent.action.MAIN" />\n' >> $@
	@printf '                <category android:name="android.intent.category.LAUNCHER" />\n' >> $@
	@printf '            </intent-filter>\n' >> $@
	@printf '        </activity>\n' >> $@
	@printf '    </application>\n' >> $@
	@printf '</manifest>\n' >> $@
# Only build classes.dex when the example needs Go→Java proxy support
# (set EXAMPLE_NEEDS_PROXY := true in the example Makefile).
HANDLER_JAVA  := $(HANDLER_DIR)/center/dx/jni/internal/GoInvocationHandler.java
DISPATCH_JAVA := $(HANDLER_DIR)/center/dx/jni/internal/GoAbstractDispatch.java
ifeq ($(EXAMPLE_NEEDS_PROXY),true)
ifneq ($(EXAMPLE_USES_MATERIAL),true)
$(BUILD)/classes.dex: $(HANDLER_JAVA) $(DISPATCH_JAVA) $(EXAMPLE_EXTRA_JAVA)
	@mkdir -p $(BUILD)/java
	javac --release 17 -classpath $(PLATFORM)/android.jar \
		-d $(BUILD)/java $(HANDLER_JAVA) $(DISPATCH_JAVA) $(EXAMPLE_EXTRA_JAVA)
	$(D8) --lib $(PLATFORM)/android.jar --output $(BUILD) \
		$$(find $(BUILD)/java -name '*.class')
APK_DEPS += $(BUILD)/classes.dex
endif
endif

$(BUILD)/lib/arm64-v8a/libexample.so: main.go
	@mkdir -p $(dir $@)
	cd ../.. && CGO_ENABLED=1 GOOS=android GOARCH=arm64 CC=$(CC_ARM64) \
		CGO_LDFLAGS="-llog -landroid" \
		go build -buildmode=c-shared \
		-o examples/$(EXAMPLE_NAME)/$@ \
		./examples/$(EXAMPLE_NAME)/
	@rm -f $(@:.so=.h)
$(BUILD)/lib/x86_64/libexample.so: main.go
	@mkdir -p $(dir $@)
	cd ../.. && CGO_ENABLED=1 GOOS=android GOARCH=amd64 CC=$(CC_AMD64) \
		CGO_LDFLAGS="-llog -landroid" \
		go build -buildmode=c-shared \
		-o examples/$(EXAMPLE_NAME)/$@ \
		./examples/$(EXAMPLE_NAME)/
	@rm -f $(@:.so=.h)
$(BUILD)/$(EXAMPLE_NAME).apk: $(BUILD)/AndroidManifest.xml $(APK_DEPS) $(BUILD)/lib/arm64-v8a/libexample.so $(BUILD)/lib/x86_64/libexample.so $(BUILD)/debug.keystore
	$(AAPT2) link --manifest $(BUILD)/AndroidManifest.xml \
		-I $(PLATFORM)/android.jar \
		--min-sdk-version $(MIN_SDK) \
		--target-sdk-version $(TARGET_SDK) \
		-o $(BUILD)/base.apk
	@if [ -f $(BUILD)/classes.dex ]; then cd $(BUILD) && zip -j base.apk classes.dex; fi
	cd $(BUILD) && zip -r base.apk lib/
	$(ZIPALIGN) -f 4 $(BUILD)/base.apk $(BUILD)/aligned.apk
	$(APKSIGNER) sign --ks $(BUILD)/debug.keystore --ks-pass pass:android \
		--out $@ $(BUILD)/aligned.apk
	@rm -f $(BUILD)/base.apk $(BUILD)/aligned.apk
	@echo "Built: $@"
install: $(BUILD)/$(EXAMPLE_NAME).apk
	$(ADB) install -r $<
run: install
	@$(foreach perm,$(EXAMPLE_PERMISSIONS), \
		$(ADB) shell pm grant $(PACKAGE_NAME) $(perm) 2>/dev/null || true;)
	$(ADB) shell am start -n $(PACKAGE_NAME)/android.app.NativeActivity
clean:
	rm -rf $(BUILD)

# Material 3 pipeline. Included at the end so its target overrides for
# classes.dex and the final APK win over the bare-NativeActivity rules above.
ifeq ($(EXAMPLE_USES_MATERIAL),true)
include $(dir $(lastword $(MAKEFILE_LIST)))material.mk
endif
