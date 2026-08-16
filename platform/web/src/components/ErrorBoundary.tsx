import { Component } from 'react';
import type { ErrorInfo, ReactNode } from 'react';

/**
 * ErrorBoundary catches a render-time exception and shows something instead of
 * nothing.
 *
 * Without one, React 18 unmounts the entire tree when any component throws, and
 * the operator gets a blank white page with no message, no navigation and no
 * indication that anything is wrong beyond the absence of everything. A blank
 * page is indistinguishable from a network failure or a bad deployment, so the
 * first thing it costs is the ability to tell what broke.
 *
 * This has to be a class. Hooks cannot catch render errors: componentDidCatch
 * and getDerivedStateFromError have no function-component equivalent.
 *
 * What it does not catch, so nobody plans around it wrongly: errors thrown in
 * event handlers, in async callbacks, or during server-side rendering. React
 * Query failures do not reach here either, and should not - those are data
 * errors with a status code, and ErrorNotice renders them with the distinction
 * between "you may not see this" and "the server is down" intact.
 */

type Props = {
  children: ReactNode;
  /** Extra context for the log line, naming where the boundary sits. */
  where?: string;
};

type State = {
  error: Error | null;
};

export class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    // The component stack is the useful half and it is not on the Error, so it
    // is logged separately. There is no error reporting service configured;
    // when there is, this is the one place that has to change.
    console.error(
      `Unhandled render error${this.props.where ? ` in ${this.props.where}` : ''}:`,
      error,
      info.componentStack,
    );
  }

  /** reset clears the error so the same subtree can try to render again. */
  private reset = () => this.setState({ error: null });

  render() {
    const { error } = this.state;
    if (!error) return this.props.children;

    return (
      <div role="alert" className="rounded-md border border-red-200 bg-red-50 p-6">
        <h2 className="text-base font-semibold text-red-800">This page did not load</h2>
        <p className="mt-2 text-sm text-red-700">
          Something in the portal failed while rendering. The rest of the application is still
          running, so navigating elsewhere should work.
        </p>
        {/* The message, not the stack. A stack on screen is noise to an
            operator and detail to anyone else looking over their shoulder;
            the stack is in the console for whoever is debugging. */}
        <p className="mt-2 font-mono text-xs text-red-700">{error.message}</p>
        <div className="mt-4 flex gap-2">
          <button
            type="button"
            onClick={this.reset}
            className="rounded-md border border-red-300 bg-white px-3 py-1.5 text-sm font-medium text-red-700 hover:bg-red-50"
          >
            Try again
          </button>
          <button
            type="button"
            onClick={() => window.location.reload()}
            className="rounded-md border border-gray-300 bg-white px-3 py-1.5 text-sm font-medium text-gray-700 hover:bg-gray-50"
          >
            Reload the page
          </button>
        </div>
      </div>
    );
  }
}
