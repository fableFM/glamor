import { RouterProvider } from '@tanstack/react-router'
import { AppProvider } from './providers/AppProvider'
import { router } from './router'

export default function App() {
  return (
    <AppProvider>
      <RouterProvider router={router} />
    </AppProvider>
  )
}
