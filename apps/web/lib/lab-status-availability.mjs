export function getLabStatusAvailability({ hasData, isFetching }) {
  return {
    isInitialLoading: !hasData && isFetching,
    statusReady: hasData,
  };
}
