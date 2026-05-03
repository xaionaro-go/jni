package center.dx.jni.internal;

/**
 * TestAbstractAdapter mirrors the shape of androidx.recyclerview.widget
 * RecyclerView.Adapter after type erasure: the ViewHolder generic
 * parameter erases to Object, and the ViewGroup parameter — being an
 * Android-only type — is also represented here as Object so the test
 * fixture can compile against a vanilla classpath.
 *
 * The three abstract methods exercise the full surface that
 * tryAbstractAdapter must support:
 *   - Object return with mixed Object/primitive params
 *   - void return with mixed Object/primitive params
 *   - primitive int return with no params
 */
public abstract class TestAbstractAdapter {
    public abstract Object onCreateViewHolder(Object parent, int viewType);
    public abstract void onBindViewHolder(Object holder, int position);
    public abstract int getItemCount();
}
