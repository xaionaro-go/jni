package center.dx.jni.generated;

import center.dx.jni.internal.GoAbstractDispatch;

/**
 * TestAbstractAdapterAdapter is the hand-written equivalent of what
 * templates/java/abstract_adapter.java.tmpl emits for
 * center.dx.jni.internal.TestAbstractAdapter. Keeping it hand-written
 * lets the test verify the dispatch path without invoking the generator.
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
