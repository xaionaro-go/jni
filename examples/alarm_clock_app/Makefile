EXAMPLE_NAME := alarm_clock_app
EXAMPLE_PERMISSIONS := android.permission.USE_EXACT_ALARM
EXAMPLE_USES_MATERIAL := true
# Generated abstract-adapter Java shims required by tryAbstractAdapter for
# the RecyclerView surface. The shims live next to the bindings; we add
# them to javac's input set so the resulting classes.dex carries the
# concrete subclass and the GoAbstractDispatch dispatch path can find them.
EXAMPLE_EXTRA_JAVA := \
	$(realpath ../../androidx/recyclerview/widget/java/RecyclerView$$AdapterAdapter.java) \
	$(realpath ../../androidx/recyclerview/widget/java/RecyclerView$$ViewHolderAdapter.java)

include ../apk.mk
