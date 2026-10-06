import { ZeaPage } from './ZeaPage';

interface ExtensionsAPI {
  registerSystemLevelExtension(component: unknown, title: string, path: string, icon: string): void;
}

declare global {
  interface Window {
    extensionsAPI: ExtensionsAPI;
  }
}

// Argo CD's App component subscribes to "systemLevel" registrations in its
// constructor and never replays earlier ones. Since Argo CD 3.5 (React 19,
// createRoot) the first render is asynchronous, so extensions.js runs before
// App exists and an immediate registration is lost. Register once #app has
// content, i.e. App has been constructed; on older Argo CD this is immediate.
function whenArgoCDMounted(callback: () => void): void {
  const root = document.getElementById('app');
  if (!root || root.childElementCount > 0) {
    callback();
    return;
  }
  const observer = new MutationObserver(() => {
    if (root.childElementCount > 0) {
      observer.disconnect();
      callback();
    }
  });
  observer.observe(root, { childList: true });
}

// Adds "Zea" to the Argo CD left sidebar (route: <argocd>/zea).
whenArgoCDMounted(() => {
  window.extensionsAPI.registerSystemLevelExtension(ZeaPage, 'Zea', '/zea', 'fa-anchor');
});
