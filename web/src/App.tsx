import {BrowserRouter, Navigate, Route, Routes} from 'react-router-dom';
import {QueryProvider} from './providers/QueryProvider';
import {ProtectedRoute} from './components/ProtectedRoute';
import Layout from './components/Layout';
import DeviceScope from './components/DeviceScope';
import Login from './pages/Login';
import OIDCCallback from './pages/OIDCCallback';
import Devices from './pages/Devices';
import DeviceEnrollment from './pages/DeviceEnrollment';
import Dashboard from './pages/Dashboard';
import Messages from './pages/Messages';
import SerialControl from './pages/SerialControl';
import NotificationChannels from './pages/NotificationChannels';
import ScheduledTasksConfig from './pages/ScheduledTasksConfig';
import AutoFlymodeSettings from './pages/AutoFlymodeSettings';
import {Toaster} from '@/components/ui/sonner';

export default function App() {
    return <QueryProvider><BrowserRouter><Routes>
        <Route path="/login" element={<Login/>}/>
        <Route path="/oidc/callback" element={<OIDCCallback/>}/>
        <Route path="/devices" element={<ProtectedRoute><Layout/></ProtectedRoute>}>
            <Route index element={<Devices/>}/>
            <Route path="enroll" element={<DeviceEnrollment/>}/>
        </Route>
        <Route path="/devices/:deviceId" element={<ProtectedRoute><DeviceScope/></ProtectedRoute>}>
            <Route index element={<Dashboard/>}/>
            <Route path="messages" element={<Messages/>}/>
            <Route path="serial" element={<SerialControl/>}/>
            <Route path="notifications" element={<NotificationChannels/>}/>
            <Route path="scheduled-tasks" element={<ScheduledTasksConfig/>}/>
            <Route path="auto-flymode" element={<AutoFlymodeSettings/>}/>
        </Route>
        <Route path="*" element={<Navigate to="/devices" replace/>}/>
    </Routes></BrowserRouter><Toaster position="top-right" richColors closeButton toastOptions={{className:'app-toast'}}/></QueryProvider>;
}