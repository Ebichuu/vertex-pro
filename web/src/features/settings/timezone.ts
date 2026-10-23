import { createContext, useContext } from 'react'

export const DisplayTimezoneContext = createContext('UTC')
export const useDisplayTimezone = () => useContext(DisplayTimezoneContext)
