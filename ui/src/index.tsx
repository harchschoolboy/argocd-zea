import { ZeaPage } from './ZeaPage';

interface ExtensionsAPI {
  registerSystemLevelExtension(component: unknown, title: string, path: string, icon: string): void;
}

declare global {
  interface Window {
    extensionsAPI: ExtensionsAPI;
  }
}

// Adds "Zea" to the Argo CD left sidebar (route: <argocd>/zea).
window.extensionsAPI.registerSystemLevelExtension(ZeaPage, 'Zea', '/zea', 'fa-anchor');
