import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { ErrorBoundary } from './ErrorBoundary';

/** Throws on every render, standing in for a component with a real bug. */
function Explodes({ message = 'kaboom' }: { message?: string }): JSX.Element {
  throw new Error(message);
}

describe('ErrorBoundary', () => {
  // React logs the caught error itself, and the boundary logs it again. Both
  // are correct and both are noise in the test output, so console.error is
  // stubbed rather than left to scroll past a real failure.
  beforeEach(() => {
    vi.spyOn(console, 'error').mockImplementation(() => {});
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('renders its children when nothing throws', () => {
    render(
      <ErrorBoundary>
        <p>the page</p>
      </ErrorBoundary>,
    );

    expect(screen.getByText('the page')).toBeInTheDocument();
  });

  // The property that matters: a throwing component gives the operator a
  // message rather than a blank page, which is what React 18 does with an
  // uncaught render error.
  it('shows a message instead of unmounting the tree', () => {
    render(
      <ErrorBoundary>
        <Explodes message="the device list is undefined" />
      </ErrorBoundary>,
    );

    expect(screen.getByRole('alert')).toBeInTheDocument();
    expect(screen.getByText('This page did not load')).toBeInTheDocument();
    expect(screen.getByText('the device list is undefined')).toBeInTheDocument();
  });

  it('logs the error and the component stack for whoever is debugging', () => {
    render(
      <ErrorBoundary where="/devices">
        <Explodes />
      </ErrorBoundary>,
    );

    const logged = vi.mocked(console.error).mock.calls.map((call) => String(call[0]));
    expect(logged.some((line) => line.includes('/devices'))).toBe(true);
  });

  // A boundary that catches once and stays broken is only marginally better
  // than the blank page. Try again has to be able to recover.
  it('re-renders the subtree when Try again is pressed', async () => {
    // The flag stands in for whatever transient condition caused the throw.
    // It has to live outside the component: the boundary unmounts the subtree,
    // so any state inside it goes with it.
    let stillBroken = true;

    function Flaky() {
      if (stillBroken) throw new Error('transient');
      return <p>recovered</p>;
    }

    render(
      <ErrorBoundary>
        <Flaky />
      </ErrorBoundary>,
    );

    expect(screen.getByRole('alert')).toBeInTheDocument();

    stillBroken = false;
    await userEvent.click(screen.getByRole('button', { name: 'Try again' }));

    expect(screen.getByText('recovered')).toBeInTheDocument();
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  });

  // And if the cause has not gone away, the boundary catches again rather than
  // letting the second throw escape to the root.
  it('catches again when Try again does not help', async () => {
    render(
      <ErrorBoundary>
        <Explodes message="still broken" />
      </ErrorBoundary>,
    );

    await userEvent.click(screen.getByRole('button', { name: 'Try again' }));

    expect(screen.getByRole('alert')).toBeInTheDocument();
    expect(screen.getByText('still broken')).toBeInTheDocument();
  });

  // The stack is for the console. On screen it is noise to an operator and
  // detail to anyone reading over their shoulder.
  it('does not print a stack trace on the page', () => {
    render(
      <ErrorBoundary>
        <Explodes />
      </ErrorBoundary>,
    );

    expect(screen.getByRole('alert').textContent).not.toContain('at Explodes');
  });
});
