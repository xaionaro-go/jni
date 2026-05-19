package center.dx.jni.generated.center.dx.jni.internal;

import center.dx.jni.internal.GoAbstractDispatch;

/**
 * TestAbstractAdapterAdapter is the target-package namespaced adapter for
 * center.dx.jni.internal.TestAbstractAdapter. The flat adapter fixture still
 * exists so tests can prove namespaced lookup is attempted first.
 */
public class TestAbstractAdapterAdapter extends center.dx.jni.internal.TestAbstractAdapter {
    private final long handlerID;

    public TestAbstractAdapterAdapter(long handlerID) {
        this.handlerID = handlerID;
    }

    @Override
    public Object onCreateViewHolder(Object parent, int viewType) {
        return GoAbstractDispatch.invoke(
            handlerID, "onCreateViewHolder", new Object[]{ parent, Integer.valueOf(viewType) });
    }

    @Override
    public void onBindViewHolder(Object holder, int position) {
        GoAbstractDispatch.invoke(
            handlerID, "onBindViewHolder", new Object[]{ holder, Integer.valueOf(position) });
    }

    @Override
    public int getItemCount() {
        return ((Integer) GoAbstractDispatch.invoke(
            handlerID, "getItemCount", new Object[]{ })).intValue();
    }
}
