import * as React from 'react';

// Route is the Zea page state kept in the URL query, so a connection view
// can be linked to and the browser Back button returns to the list.
export interface Route {
  // "streams" (default), "connections" or "registries" (admins).
  view?: string;
  connection?: string;
  ref?: string;
  run?: string;
  // Connection view tab: "images"; runs by default.
  tab?: string;
  // Selected Stream; with mode "edit" its editor, with srun one of its runs.
  stream?: string;
  // "edit" opens the Stream editor, "new" a new Stream, "run" the run form.
  mode?: string;
  srun?: string;
}

const KEYS: (keyof Route)[] = ['view', 'connection', 'ref', 'run', 'tab', 'stream', 'mode', 'srun'];
const NAVIGATE_EVENT = 'zea:navigate';

export function readRoute(): Route {
  const params = new URLSearchParams(window.location.search);
  const route: Route = {};
  for (const k of KEYS) {
    const v = params.get(k);
    if (v) {
      route[k] = v;
    }
  }
  return route;
}

export function routeHref(route: Route): string {
  const url = new URL(window.location.href);
  for (const k of KEYS) {
    const v = route[k];
    if (v) {
      url.searchParams.set(k, v);
    } else {
      url.searchParams.delete(k);
    }
  }
  return url.pathname + url.search + url.hash;
}

export function navigate(route: Route) {
  window.history.pushState(window.history.state, '', routeHref(route));
  window.dispatchEvent(new Event(NAVIGATE_EVENT));
  window.scrollTo(0, 0);
}

export function useRoute(): Route {
  const [route, setRoute] = React.useState(readRoute);
  React.useEffect(() => {
    const update = () => setRoute(readRoute());
    window.addEventListener('popstate', update);
    window.addEventListener(NAVIGATE_EVENT, update);
    return () => {
      window.removeEventListener('popstate', update);
      window.removeEventListener(NAVIGATE_EVENT, update);
    };
  }, []);
  return route;
}
