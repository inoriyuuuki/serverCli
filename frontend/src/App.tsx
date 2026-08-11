import { Navigate, Route, Routes } from 'react-router-dom';
import { AuthProvider } from './auth/AuthContext';
import { RequireAuth, RequirePrimary } from './auth/ProtectedRoute';
import { ConfirmProvider } from './components/ui';
import { Layout } from './components/Layout';
import LoginPage from './pages/LoginPage';
import OverviewPage from './pages/OverviewPage';
import ServersPage from './pages/ServersPage';
import ServerDetailPage from './pages/ServerDetailPage';
import MigrationConsolePage from './pages/MigrationConsolePage';
import CommandCenterPage from './pages/CommandCenterPage';
import TasksPage from './pages/TasksPage';
import TaskDetailPage from './pages/TaskDetailPage';
import AiCredentialsPage from './pages/AiCredentialsPage';
import ApiTokensPage from './pages/ApiTokensPage';
import ApiDirectoryPage from './pages/ApiDirectoryPage';
import AuditLogsPage from './pages/AuditLogsPage';
import SettingsPage from './pages/SettingsPage';
import ClustersPage from './pages/ClustersPage';
import ClusterDetailPage from './pages/ClusterDetailPage';
import OperationsPage from './pages/OperationsPage';
import ReleaseCachePage from './pages/ReleaseCachePage';
import PrimaryTransferPage from './pages/PrimaryTransferPage';

function AppRoutes() {
  return (
    <Routes>
      <Route path="/login" element={<LoginPage />} />
      <Route
        element={
          <RequireAuth>
            <Layout />
          </RequireAuth>
        }
      >
        <Route index element={<OverviewPage />} />
        <Route
          path="servers"
          element={
            <RequirePrimary>
              <ServersPage />
            </RequirePrimary>
          }
        />
        <Route
          path="servers/:nodeId"
          element={
            <RequirePrimary>
              <ServerDetailPage />
            </RequirePrimary>
          }
        />
        <Route path="migrate" element={<MigrationConsolePage />} />
        <Route path="clusters" element={<ClustersPage />} />
        <Route path="clusters/:id" element={<ClusterDetailPage />} />
        <Route path="operations" element={<OperationsPage />} />
        <Route path="release-cache" element={<ReleaseCachePage />} />
        <Route path="primary-transfers" element={<PrimaryTransferPage />} />
        <Route path="commands" element={<CommandCenterPage />} />
        <Route path="tasks" element={<TasksPage />} />
        <Route path="tasks/:taskId" element={<TaskDetailPage />} />
        <Route path="ai-credentials" element={<AiCredentialsPage />} />
        <Route
          path="api-tokens"
          element={
            <RequirePrimary>
              <ApiTokensPage />
            </RequirePrimary>
          }
        />
        <Route
          path="api-directory"
          element={
            <RequirePrimary>
              <ApiDirectoryPage />
            </RequirePrimary>
          }
        />
        <Route path="audit" element={<AuditLogsPage />} />
        <Route path="settings" element={<SettingsPage />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Route>
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  );
}

export default function App() {
  return (
    <AuthProvider>
      <ConfirmProvider>
        <AppRoutes />
      </ConfirmProvider>
    </AuthProvider>
  );
}
